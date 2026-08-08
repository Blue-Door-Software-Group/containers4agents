package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	apierrors "github.com/Blue-Door-Software-Group/containers4agents/internal/api/errors"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/api/middleware"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/lifecycle"
)

type ContainerService interface {
	CreateContainer(ctx context.Context) (*lifecycle.ContainerView, error)
	GetContainer(ctx context.Context, externalID string) (*lifecycle.ContainerView, error)
	StopContainer(ctx context.Context, externalID string) (*lifecycle.ContainerView, error)
}

type ContainerHandler struct {
	service ContainerService
}

func NewContainerHandler(service ContainerService) *ContainerHandler {
	return &ContainerHandler{service: service}
}

type containerResponse struct {
	ID             string  `json:"id"`
	State          string  `json:"state"`
	Profile        string  `json:"profile"`
	ImageVersion   string  `json:"image_version"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
	LastActivityAt *string `json:"last_activity_at,omitempty"`
}

func (h *ContainerHandler) Create(w http.ResponseWriter, r *http.Request) {
	row, err := h.service.CreateContainer(r.Context())
	if err != nil {
		writeLifecycleError(w, r, err)
		return
	}
	writeContainerResponse(w, http.StatusCreated, row)
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

	row, err := h.service.StopContainer(r.Context(), externalID)
	if err != nil {
		writeLifecycleError(w, r, err)
		return
	}
	writeContainerResponse(w, http.StatusOK, row)
}

func writeContainerResponse(w http.ResponseWriter, status int, row *lifecycle.ContainerView) {
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

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}

func writeLifecycleError(w http.ResponseWriter, r *http.Request, err error) {
	requestID := middleware.GetRequestID(r.Context())

	switch {
	case errors.Is(err, lifecycle.ErrContainerNotFound):
		apierrors.Write(w, requestID, apierrors.NotFound("container not found"))
	case errors.Is(err, lifecycle.ErrStateConflict):
		apierrors.Write(w, requestID, apierrors.Conflict(err.Error()))
	default:
		apierrors.Write(w, requestID, apierrors.Internal("internal lifecycle error"))
	}
}
