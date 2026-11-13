package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	apierrors "github.com/Blue-Door-Software-Group/containers4agents/internal/api/errors"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/api/middleware"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/idempotency"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/lifecycle"
	obslog "github.com/Blue-Door-Software-Group/containers4agents/internal/observability/logging"
)

type ContainerService interface {
	CreateContainer(ctx context.Context) (*lifecycle.ContainerView, error)
	GetContainer(ctx context.Context, externalID string) (*lifecycle.ContainerView, error)
	StopContainer(ctx context.Context, externalID string) (*lifecycle.ContainerView, error)
	StartContainer(ctx context.Context, externalID string) (*lifecycle.ContainerView, error)
	PruneContainer(ctx context.Context, externalID string) (*lifecycle.ContainerView, error)
}

type ContainerHandler struct {
	service ContainerService
	idem    *idempotency.Service
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
	CreatedAt                string  `json:"created_at"`
	UpdatedAt                string  `json:"updated_at"`
	LastActivityAt           *string `json:"last_activity_at,omitempty"`
	IdleStopDeadlineAt       *string `json:"idle_stop_deadline_at,omitempty"`
	RetentionPruneDeadlineAt *string `json:"retention_prune_deadline_at,omitempty"`
	StoppedAt                *string `json:"stopped_at,omitempty"`
}

func (h *ContainerHandler) Create(w http.ResponseWriter, r *http.Request) {
	idemKey := r.Header.Get(idempotency.HeaderName)
	scope := "containers:create"
	requestHash := idempotency.Hash(r.Method, r.URL.Path)
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

	row, err := h.service.CreateContainer(r.Context())
	if err != nil {
		status := writeLifecycleError(w, r, err)
		h.completeIdempotent(r.Context(), "containers.create", scope, idemKey, requestHash, status, struct{}{})
		return
	}
	resp := makeContainerResponse(row)
	status := http.StatusCreated
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
