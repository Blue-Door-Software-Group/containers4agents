package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	apierrors "github.com/Blue-Door-Software-Group/containers4agents/internal/api/errors"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/api/middleware"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/files"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/idempotency"
	obslog "github.com/Blue-Door-Software-Group/containers4agents/internal/observability/logging"
)

type FileService interface {
	Upload(ctx context.Context, externalID string, requestedPath string, content io.Reader) error
	Download(ctx context.Context, externalID string, requestedPath string, dst io.Writer) error
	Delete(ctx context.Context, externalID string, requestedPath string) error
	Manifest(ctx context.Context, externalID string) (*files.Manifest, error)
}

type FileHandler struct {
	service        FileService
	maxUploadBytes int
	idem           *idempotency.Service
}

func NewFileHandler(service FileService, maxUploadBytes int, idem ...*idempotency.Service) *FileHandler {
	var idemSvc *idempotency.Service
	if len(idem) > 0 {
		idemSvc = idem[0]
	}
	return &FileHandler{
		service:        service,
		maxUploadBytes: maxUploadBytes,
		idem:           idemSvc,
	}
}

type manifestResponse struct {
	ID                 string                  `json:"id"`
	WorkspaceBytesUsed int64                   `json:"workspace_bytes_used"`
	WorkspaceDiskQuota int64                   `json:"workspace_disk_quota"`
	WorkspaceFileCount int64                   `json:"workspace_file_count"`
	Entries            []manifestEntryResponse `json:"entries"`
}

type manifestEntryResponse struct {
	Path      string                `json:"path"`
	Name      string                `json:"name"`
	Type      string                `json:"type"`
	SizeBytes int64                 `json:"size_bytes"`
	MTime     string                `json:"mtime"`
	Hash      *manifestHashResponse `json:"hash,omitempty"`
}

type manifestHashResponse struct {
	Algorithm string `json:"algorithm"`
	Value     string `json:"value"`
}

func (h *FileHandler) Upload(w http.ResponseWriter, r *http.Request) {
	externalID := r.PathValue("id")
	requestedPath := r.PathValue("path")
	if externalID == "" {
		apierrors.Write(w, middleware.GetRequestID(r.Context()), apierrors.BadRequest("missing container id"))
		return
	}
	if requestedPath == "" {
		apierrors.Write(w, middleware.GetRequestID(r.Context()), apierrors.BadRequest("missing file path"))
		return
	}

	idemKey := r.Header.Get(idempotency.HeaderName)
	scope := fmt.Sprintf("files:upload:%s:%s", externalID, requestedPath)
	var uploadReader io.Reader
	var requestHash string
	var stagedFile *os.File
	var cleanup func()

	if idemKey != "" {
		var stageErr error
		stagedFile, requestHash, stageErr = h.stageUploadForIdempotency(r)
		if stageErr != nil {
			writeFileError(w, r, stageErr)
			return
		}
		cleanup = func() {
			_ = stagedFile.Close()
			_ = os.Remove(stagedFile.Name())
		}
		defer cleanup()

		begin, ok := h.beginIdempotent(w, r, "files.upload", scope, idemKey, requestHash)
		if !ok {
			return
		}
		if begin != nil && begin.Replay {
			writeReplay(w, begin.StatusCode, begin.ResponseBody)
			return
		}
		if _, err := stagedFile.Seek(0, io.SeekStart); err != nil {
			apierrors.Write(w, middleware.GetRequestID(r.Context()), apierrors.Internal("failed to rewind upload payload"))
			return
		}
		uploadReader = stagedFile
	} else {
		requestHash = idempotency.Hash(r.Method, r.URL.Path, r.Header.Get("Content-Length"))
		uploadReader = io.LimitReader(r.Body, int64(h.maxUploadBytes)+1)
	}

	if err := h.service.Upload(r.Context(), externalID, requestedPath, uploadReader); err != nil {
		writeFileError(w, r, err)
		return
	}

	respBody := "{}"
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write([]byte(respBody))
	h.completeIdempotent(r.Context(), "files.upload", scope, idemKey, requestHash, http.StatusCreated, respBody)
}

func (h *FileHandler) Download(w http.ResponseWriter, r *http.Request) {
	externalID := r.PathValue("id")
	requestedPath := r.PathValue("path")
	if externalID == "" {
		apierrors.Write(w, middleware.GetRequestID(r.Context()), apierrors.BadRequest("missing container id"))
		return
	}
	if requestedPath == "" {
		apierrors.Write(w, middleware.GetRequestID(r.Context()), apierrors.BadRequest("missing file path"))
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	tracker := &streamWriteTracker{writer: w}
	err := h.service.Download(r.Context(), externalID, requestedPath, tracker)
	if err != nil {
		if !tracker.wrote {
			writeFileError(w, r, err)
			return
		}
		obslog.WithRequestID(r.Context(), slog.Default()).WarnContext(
			r.Context(),
			"file download stream failed after response write started",
			"component", "api_files",
			"operation", "download",
			"error_category", obslog.ErrorCategory(err),
		)
	}
}

func (h *FileHandler) Delete(w http.ResponseWriter, r *http.Request) {
	externalID := r.PathValue("id")
	requestedPath := r.PathValue("path")
	if externalID == "" {
		apierrors.Write(w, middleware.GetRequestID(r.Context()), apierrors.BadRequest("missing container id"))
		return
	}
	if requestedPath == "" {
		apierrors.Write(w, middleware.GetRequestID(r.Context()), apierrors.BadRequest("missing file path"))
		return
	}

	if err := h.service.Delete(r.Context(), externalID, requestedPath); err != nil {
		writeFileError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *FileHandler) Manifest(w http.ResponseWriter, r *http.Request) {
	externalID := r.PathValue("id")
	if externalID == "" {
		apierrors.Write(w, middleware.GetRequestID(r.Context()), apierrors.BadRequest("missing container id"))
		return
	}

	manifest, err := h.service.Manifest(r.Context(), externalID)
	if err != nil {
		writeFileError(w, r, err)
		return
	}

	entries := make([]manifestEntryResponse, 0, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		respEntry := manifestEntryResponse{
			Path:      entry.Path,
			Name:      entry.Name,
			Type:      entry.Type,
			SizeBytes: entry.SizeBytes,
			MTime:     entry.ModTime.UTC().Format(time.RFC3339),
		}
		if entry.Hash != nil {
			respEntry.Hash = &manifestHashResponse{
				Algorithm: entry.Hash.Algorithm,
				Value:     entry.Hash.Value,
			}
		}
		entries = append(entries, respEntry)
	}

	resp := manifestResponse{
		ID:                 manifest.ContainerID,
		WorkspaceBytesUsed: manifest.WorkspaceBytesUsed,
		WorkspaceDiskQuota: manifest.WorkspaceDiskQuota,
		WorkspaceFileCount: manifest.WorkspaceFileCount,
		Entries:            entries,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func writeFileError(w http.ResponseWriter, r *http.Request, err error) {
	requestID := middleware.GetRequestID(r.Context())
	obslog.WithRequestID(r.Context(), slog.Default()).WarnContext(
		r.Context(),
		"file request failed",
		"component", "api_files",
		"operation", r.Method,
		"error_category", obslog.ErrorCategory(err),
		"error_summary", obslog.ErrorSummary(err),
	)

	switch {
	case errors.Is(err, files.ErrPathViolation):
		apierrors.Write(w, requestID, apierrors.BadRequest("invalid workspace path"))
	case errors.Is(err, files.ErrDiskQuotaExceeded):
		apierrors.Write(w, requestID, apierrors.PayloadTooLarge("workspace disk quota exceeded"))
	case errors.Is(err, files.ErrPayloadTooLarge):
		apierrors.Write(w, requestID, apierrors.PayloadTooLarge("file payload exceeds configured limit"))
	case errors.Is(err, files.ErrContainerNotFound), errors.Is(err, files.ErrFileNotFound):
		apierrors.Write(w, requestID, apierrors.NotFound("resource not found"))
	case errors.Is(err, files.ErrStateConflict):
		apierrors.Write(w, requestID, apierrors.Conflict(err.Error()))
	case errors.Is(err, files.ErrProviderFailure):
		apierrors.Write(w, requestID, apierrors.ServiceUnavailable("container provider unavailable"))
	default:
		apierrors.Write(w, requestID, apierrors.Internal("internal file operation error"))
	}
}

type streamWriteTracker struct {
	writer io.Writer
	wrote  bool
}

func (h *FileHandler) beginIdempotent(
	w http.ResponseWriter,
	r *http.Request,
	operation string,
	scope string,
	key string,
	requestHash string,
) (*idempotency.BeginResult, bool) {
	if h.idem == nil || key == "" {
		return nil, true
	}
	begin, err := h.idem.Begin(r.Context(), operation, scope, key, requestHash)
	if err != nil {
		switch {
		case errors.Is(err, idempotency.ErrHashMismatch):
			apierrors.Write(w, middleware.GetRequestID(r.Context()), apierrors.IdempotencyConflict("idempotency key reused with a different request payload"))
		case errors.Is(err, idempotency.ErrInProgress):
			apierrors.Write(w, middleware.GetRequestID(r.Context()), apierrors.IdempotencyConflict("idempotency key request is already in progress"))
		case errors.Is(err, idempotency.ErrInvalidOperation), errors.Is(err, idempotency.ErrInvalidScope), errors.Is(err, idempotency.ErrInvalidRequestHash):
			apierrors.Write(w, middleware.GetRequestID(r.Context()), apierrors.UnprocessableEntity("invalid idempotency request"))
		default:
			apierrors.Write(w, middleware.GetRequestID(r.Context()), apierrors.Internal("idempotency enforcement failed"))
		}
		return nil, false
	}
	return begin, true
}

func (h *FileHandler) completeIdempotent(
	ctx context.Context,
	operation string,
	scope string,
	key string,
	requestHash string,
	status int,
	responseBody string,
) {
	if h.idem == nil || key == "" {
		return
	}
	if err := h.idem.Complete(ctx, operation, scope, key, requestHash, status, responseBody); err != nil {
		obslog.WithRequestID(ctx, slog.Default()).WarnContext(ctx, "failed to persist file idempotency response", "component", "api_files", "operation", operation, "error_category", obslog.ErrorCategory(err))
	}
}

func (h *FileHandler) stageUploadForIdempotency(r *http.Request) (*os.File, string, error) {
	tmp, err := os.CreateTemp("", "c4a-upload-*")
	if err != nil {
		return nil, "", fmt.Errorf("%w: create temp upload file", files.ErrProviderFailure)
	}

	hash := sha256.New()
	reader := io.LimitReader(r.Body, int64(h.maxUploadBytes)+1)
	written, copyErr := io.Copy(io.MultiWriter(tmp, hash), reader)
	if copyErr != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return nil, "", fmt.Errorf("%w: read upload payload", files.ErrProviderFailure)
	}
	if written > int64(h.maxUploadBytes) {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return nil, "", files.ErrPayloadTooLarge
	}

	bodyHash := hex.EncodeToString(hash.Sum(nil))
	requestHash := idempotency.Hash(r.Method, r.URL.Path, bodyHash, r.Header.Get("Content-Type"))
	return tmp, requestHash, nil
}

func (t *streamWriteTracker) Write(p []byte) (int, error) {
	n, err := t.writer.Write(p)
	if n > 0 {
		t.wrote = true
	}
	return n, err
}
