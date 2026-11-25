package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	apierrors "github.com/Blue-Door-Software-Group/containers4agents/internal/api/errors"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/api/middleware"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/idempotency"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/lifecycle"
	obslog "github.com/Blue-Door-Software-Group/containers4agents/internal/observability/logging"
)

type ContainerService interface {
	CreateContainer(ctx context.Context, opts lifecycle.CreateOptions) (*lifecycle.ContainerView, error)
	GetContainer(ctx context.Context, externalID string) (*lifecycle.ContainerView, error)
	StopContainer(ctx context.Context, externalID string) (*lifecycle.ContainerView, error)
	StartContainer(ctx context.Context, externalID string) (*lifecycle.ContainerView, error)
	PruneContainer(ctx context.Context, externalID string) (*lifecycle.ContainerView, error)
}

type ContainerHandler struct {
	service ContainerService
	idem    *idempotency.Service
	images  []ImageInfo
}

// ImageInfo is one registered image as GET /v1/images lists it: never its ref or credentials.
type ImageInfo struct {
	Name                    string `json:"name"`
	Default                 bool   `json:"default"`
	Profile                 string `json:"profile"`
	Network                 string `json:"network"`
	IdleStopTimeout         string `json:"idle_stop_timeout"`
	StoppedRetentionTimeout string `json:"stopped_retention_timeout"`
}

// WithImages sets the registered images GET /v1/images lists.
func (h *ContainerHandler) WithImages(images []ImageInfo) *ContainerHandler {
	h.images = images
	return h
}

// Images is GET /v1/images: the images a create may name.
func (h *ContainerHandler) Images(w http.ResponseWriter, _ *http.Request) {
	images := h.images
	if images == nil {
		images = []ImageInfo{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"images": images})
}

func NewContainerHandler(service ContainerService, idem ...*idempotency.Service) *ContainerHandler {
	var idemSvc *idempotency.Service
	if len(idem) > 0 {
		idemSvc = idem[0]
	}
	return &ContainerHandler{service: service, idem: idemSvc}
}

type containerResponse struct {
	ID                       string  `json:"id"`
	State                    string  `json:"state"`
	Profile                  string  `json:"profile"`
	ImageVersion             string  `json:"image_version"`
	Image                    string  `json:"image,omitempty"`
	Network                  string  `json:"network,omitempty"`
	CreatedAt                string  `json:"created_at"`
	UpdatedAt                string  `json:"updated_at"`
	LastActivityAt           *string `json:"last_activity_at,omitempty"`
	IdleStopDeadlineAt       *string `json:"idle_stop_deadline_at,omitempty"`
	RetentionPruneDeadlineAt *string `json:"retention_prune_deadline_at,omitempty"`
	StoppedAt                *string `json:"stopped_at,omitempty"`
}

// createRequest is POST /v1/containers' optional body. Without one the create is the one before
// images: the default image and a server-generated id.
type createRequest struct {
	Image      string `json:"image,omitempty"`
	ExternalID string `json:"external_id,omitempty"`
	IfExists   string `json:"if_exists,omitempty"`
}

// externalIDPattern is what a caller-chosen container id may look like.
var externalIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// maxCreateBodyBytes caps the create body.
const maxCreateBodyBytes = 4 << 10

// parseCreateRequest reads the optional create body. hashParts are what the idempotency hash
// covers beyond method and path: nothing for an empty body (so pre-body keys replay as before),
// the canonical body otherwise.
func parseCreateRequest(r *http.Request) (req createRequest, hashParts []string, msg string) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxCreateBodyBytes+1))
	if err != nil {
		return req, nil, "unreadable request body"
	}
	if len(raw) > maxCreateBodyBytes {
		return req, nil, "request body too large"
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return req, nil, ""
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return req, nil, "invalid create request body"
	}
	req.Image = strings.TrimSpace(req.Image)
	req.ExternalID = strings.TrimSpace(req.ExternalID)
	if req.ExternalID != "" && !externalIDPattern.MatchString(req.ExternalID) {
		return req, nil, "external_id must match ^[A-Za-z0-9._:-]{1,128}$"
	}
	switch req.IfExists {
	case "", "return":
	default:
		return req, nil, `if_exists must be "return" when given`
	}
	canonical, _ := json.Marshal(req)
	if string(canonical) == "{}" {
		return req, nil, ""
	}
	return req, []string{string(canonical)}, ""
}

func (h *ContainerHandler) Create(w http.ResponseWriter, r *http.Request) {
	body, hashParts, msg := parseCreateRequest(r)
	if msg != "" {
		apierrors.Write(w, middleware.GetRequestID(r.Context()), apierrors.BadRequest(msg))
		return
	}
	idemKey := r.Header.Get(idempotency.HeaderName)
	scope := "containers:create"
	requestHash := idempotency.Hash(append([]string{r.Method, r.URL.Path}, hashParts...)...)
	begin, ok := h.beginIdempotent(w, r, "containers.create", scope, idemKey, requestHash)
	if !ok {
		return
	}
	if begin != nil && begin.Replay {
		replaySafe, replayErr := h.replayCreateResponseIfLive(r.Context(), begin, w)
		if replayErr != nil {
			writeLifecycleError(w, r, replayErr)
			return
		}
		if replaySafe {
			return
		}
	}

	row, err := h.service.CreateContainer(r.Context(), lifecycle.CreateOptions{
		Image:          body.Image,
		ExternalID:     body.ExternalID,
		ReturnExisting: body.IfExists == "return",
	})
	var exists *lifecycle.ExistsError
	switch {
	case errors.As(err, &exists):
		writeJSON(w, http.StatusConflict, map[string]any{
			"code":       "container_exists",
			"message":    "a container with this external_id already exists",
			"request_id": middleware.GetRequestID(r.Context()),
			"container":  makeContainerResponse(exists.View),
		})
		h.completeIdempotent(r.Context(), "containers.create", scope, idemKey, requestHash, http.StatusConflict, struct{}{})
		return
	case errors.Is(err, lifecycle.ErrUnknownImage):
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"code":       "unknown_image",
			"message":    err.Error(),
			"request_id": middleware.GetRequestID(r.Context()),
		})
		h.completeIdempotent(r.Context(), "containers.create", scope, idemKey, requestHash, http.StatusBadRequest, struct{}{})
		return
	case err != nil:
		status := writeLifecycleError(w, r, err)
		h.completeIdempotent(r.Context(), "containers.create", scope, idemKey, requestHash, status, struct{}{})
		return
	}
	resp := makeContainerResponse(row)
	status := http.StatusCreated
	if row.Existed {
		status = http.StatusOK
	}
	writeJSON(w, status, resp)
	h.completeIdempotent(r.Context(), "containers.create", scope, idemKey, requestHash, status, resp)
}

func (h *ContainerHandler) replayCreateResponseIfLive(ctx context.Context, begin *idempotency.BeginResult, w http.ResponseWriter) (bool, error) {
	if begin == nil || !begin.Replay {
		return false, nil
	}

	var replayed containerResponse
	if err := json.Unmarshal([]byte(begin.ResponseBody), &replayed); err != nil {
		return false, nil
	}
	containerID := replayed.ID
	if containerID == "" {
		return false, nil
	}

	_, err := h.service.GetContainer(ctx, containerID)
	if err == nil {
		writeReplay(w, begin.StatusCode, begin.ResponseBody)
		return true, nil
	}
	if errors.Is(err, lifecycle.ErrContainerNotFound) {
		return false, nil
	}
	return false, err
}

func (h *ContainerHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	externalID := r.PathValue("id")
	if externalID == "" {
		apierrors.Write(w, middleware.GetRequestID(r.Context()), apierrors.BadRequest("missing container id"))
		return
	}

	row, err := h.service.GetContainer(r.Context(), externalID)
	if err != nil {
		writeLifecycleError(w, r, err)
		return
	}
	writeContainerResponse(w, http.StatusOK, row)
}

func (h *ContainerHandler) Stop(w http.ResponseWriter, r *http.Request) {
	externalID := r.PathValue("id")
	if externalID == "" {
		apierrors.Write(w, middleware.GetRequestID(r.Context()), apierrors.BadRequest("missing container id"))
		return
	}

	idemKey := r.Header.Get(idempotency.HeaderName)
	scope := fmt.Sprintf("containers:stop:%s", externalID)
	requestHash := idempotency.Hash(r.Method, r.URL.Path)
	begin, ok := h.beginIdempotent(w, r, "containers.stop", scope, idemKey, requestHash)
	if !ok {
		return
	}
	if begin != nil && begin.Replay {
		writeReplay(w, begin.StatusCode, begin.ResponseBody)
		return
	}

	row, err := h.service.StopContainer(r.Context(), externalID)
	if err != nil {
		status := writeLifecycleError(w, r, err)
		h.completeIdempotent(r.Context(), "containers.stop", scope, idemKey, requestHash, status, struct{}{})
		return
	}
	resp := makeContainerResponse(row)
	status := http.StatusOK
	writeJSON(w, status, resp)
	h.completeIdempotent(r.Context(), "containers.stop", scope, idemKey, requestHash, status, resp)
}

func (h *ContainerHandler) Start(w http.ResponseWriter, r *http.Request) {
	externalID := r.PathValue("id")
	if externalID == "" {
		apierrors.Write(w, middleware.GetRequestID(r.Context()), apierrors.BadRequest("missing container id"))
		return
	}

	idemKey := r.Header.Get(idempotency.HeaderName)
	scope := fmt.Sprintf("containers:start:%s", externalID)
	requestHash := idempotency.Hash(r.Method, r.URL.Path)
	begin, ok := h.beginIdempotent(w, r, "containers.start", scope, idemKey, requestHash)
	if !ok {
		return
	}
	if begin != nil && begin.Replay {
		writeReplay(w, begin.StatusCode, begin.ResponseBody)
		return
	}

	row, err := h.service.StartContainer(r.Context(), externalID)
	if err != nil {
		status := writeLifecycleError(w, r, err)
		h.completeIdempotent(r.Context(), "containers.start", scope, idemKey, requestHash, status, struct{}{})
		return
	}
	resp := makeContainerResponse(row)
	status := http.StatusOK
	writeJSON(w, status, resp)
	h.completeIdempotent(r.Context(), "containers.start", scope, idemKey, requestHash, status, resp)
}

func (h *ContainerHandler) Prune(w http.ResponseWriter, r *http.Request) {
	externalID := r.PathValue("id")
	if externalID == "" {
		apierrors.Write(w, middleware.GetRequestID(r.Context()), apierrors.BadRequest("missing container id"))
		return
	}

	idemKey := r.Header.Get(idempotency.HeaderName)
	scope := fmt.Sprintf("containers:prune:%s", externalID)
	requestHash := idempotency.Hash(r.Method, r.URL.Path)
	begin, ok := h.beginIdempotent(w, r, "containers.prune", scope, idemKey, requestHash)
	if !ok {
		return
	}
	if begin != nil && begin.Replay {
		writeReplay(w, begin.StatusCode, begin.ResponseBody)
		return
	}

	row, err := h.service.PruneContainer(r.Context(), externalID)
	if err != nil {
		status := writeLifecycleError(w, r, err)
		h.completeIdempotent(r.Context(), "containers.prune", scope, idemKey, requestHash, status, struct{}{})
		return
	}
	resp := makeContainerResponse(row)
	status := http.StatusOK
	writeJSON(w, status, resp)
	h.completeIdempotent(r.Context(), "containers.prune", scope, idemKey, requestHash, status, resp)
}

func writeContainerResponse(w http.ResponseWriter, status int, row *lifecycle.ContainerView) {
	resp := makeContainerResponse(row)
	writeJSON(w, status, resp)
}

func makeContainerResponse(row *lifecycle.ContainerView) containerResponse {
	resp := containerResponse{
		ID:           row.ExternalID,
		State:        row.State,
		Profile:      row.Profile,
		ImageVersion: row.ImageVersion,
		Image:        row.ImageName,
		Network:      row.Network,
		CreatedAt:    row.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:    row.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if row.LastActivityAt != nil {
		formatted := row.LastActivityAt.UTC().Format(time.RFC3339Nano)
		resp.LastActivityAt = &formatted
	}
	if row.IdleStopDeadlineAt != nil {
		formatted := row.IdleStopDeadlineAt.UTC().Format(time.RFC3339Nano)
		resp.IdleStopDeadlineAt = &formatted
	}
	if row.RetentionPruneDeadlineAt != nil {
		formatted := row.RetentionPruneDeadlineAt.UTC().Format(time.RFC3339Nano)
		resp.RetentionPruneDeadlineAt = &formatted
	}
	if row.StoppedAt != nil {
		formatted := row.StoppedAt.UTC().Format(time.RFC3339Nano)
		resp.StoppedAt = &formatted
	}
	return resp
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeReplay(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func (h *ContainerHandler) beginIdempotent(
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

func (h *ContainerHandler) completeIdempotent(
	ctx context.Context,
	operation string,
	scope string,
	key string,
	requestHash string,
	status int,
	payload any,
) {
	if h.idem == nil || key == "" {
		return
	}
	bytes, err := json.Marshal(payload)
	if err != nil {
		obslog.WithRequestID(ctx, slog.Default()).WarnContext(ctx, "failed to marshal idempotent response payload", "component", "api_containers", "operation", operation, "error_category", obslog.ErrorCategory(err))
		return
	}
	if err := h.idem.Complete(ctx, operation, scope, key, requestHash, status, string(bytes)); err != nil {
		obslog.WithRequestID(ctx, slog.Default()).WarnContext(ctx, "failed to persist idempotency response", "component", "api_containers", "operation", operation, "error_category", obslog.ErrorCategory(err))
	}
}

// writeLifecycleError writes the API error for err and returns the HTTP status
// written, so idempotent handlers can record the failed attempt.
func writeLifecycleError(w http.ResponseWriter, r *http.Request, err error) int {
	requestID := middleware.GetRequestID(r.Context())
	obslog.WithRequestID(r.Context(), slog.Default()).WarnContext(
		r.Context(),
		"container lifecycle request failed",
		"component", "api_containers",
		"operation", r.Method,
		"error_category", obslog.ErrorCategory(err),
		"error_summary", obslog.ErrorSummary(err),
	)

	var apiErr apierrors.APIError
	switch {
	case errors.Is(err, lifecycle.ErrContainerNotFound):
		apiErr = apierrors.NotFound("container not found")
	case errors.Is(err, lifecycle.ErrStateConflict):
		apiErr = apierrors.Conflict(err.Error())
	case errors.Is(err, lifecycle.ErrCapacityExceeded):
		apiErr = apierrors.CapacityExceeded(err.Error())
	case errors.Is(err, lifecycle.ErrProviderFailure):
		apiErr = apierrors.ServiceUnavailable("container provider unavailable")
	default:
		apiErr = apierrors.Internal("internal lifecycle error")
	}
	apierrors.Write(w, requestID, apiErr)
	return apiErr.Status
}
