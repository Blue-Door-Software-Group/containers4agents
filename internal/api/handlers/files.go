package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	apierrors "github.com/Blue-Door-Software-Group/containers4agents/internal/api/errors"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/api/middleware"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/files"
)

type FileService interface {
	Upload(ctx context.Context, externalID string, requestedPath string, content []byte) error
	Download(ctx context.Context, externalID string, requestedPath string) ([]byte, error)
	Delete(ctx context.Context, externalID string, requestedPath string) error
	Manifest(ctx context.Context, externalID string) (*files.Manifest, error)
}

type FileHandler struct {
	service        FileService
	maxUploadBytes int
}

func NewFileHandler(service FileService, maxUploadBytes int) *FileHandler {
	return &FileHandler{
		service:        service,
		maxUploadBytes: maxUploadBytes,
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

	content, err := io.ReadAll(io.LimitReader(r.Body, int64(h.maxUploadBytes)+1))
	if err != nil {
		apierrors.Write(w, middleware.GetRequestID(r.Context()), apierrors.BadRequest("invalid upload request body"))
		return
	}
	if len(content) > h.maxUploadBytes {
		apierrors.Write(w, middleware.GetRequestID(r.Context()), apierrors.PayloadTooLarge("upload exceeds configured size limit"))
		return
	}

	if err := h.service.Upload(r.Context(), externalID, requestedPath, content); err != nil {
		writeFileError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusCreated)
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

	content, err := h.service.Download(r.Context(), externalID, requestedPath)
	if err != nil {
		writeFileError(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
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

	switch {
	case errors.Is(err, files.ErrPathViolation):
		apierrors.Write(w, requestID, apierrors.BadRequest("invalid workspace path"))
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
