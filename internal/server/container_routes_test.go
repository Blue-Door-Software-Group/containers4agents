package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	apierrors "github.com/Blue-Door-Software-Group/containers4agents/internal/api/errors"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/api/handlers"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/api/middleware"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/lifecycle"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/observability/metrics"
)

type staticAuthorizer struct {
	allow bool
}

func (a staticAuthorizer) IsAuthorized(_ string) bool {
	return a.allow
}

type fakeContainerService struct{}

func (f fakeContainerService) CreateContainer(_ context.Context) (*lifecycle.ContainerView, error) {
	now := time.Now().UTC()
	return &lifecycle.ContainerView{
		ExternalID:   "test-container",
		State:        "running",
		Profile:      "prod-strict",
		ImageVersion: "v1",
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

func (f fakeContainerService) GetContainer(_ context.Context, externalID string) (*lifecycle.ContainerView, error) {
	now := time.Now().UTC()
	return &lifecycle.ContainerView{
		ExternalID:   externalID,
		State:        "running",
		Profile:      "prod-strict",
		ImageVersion: "v1",
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

func (f fakeContainerService) StopContainer(_ context.Context, externalID string) (*lifecycle.ContainerView, error) {
	now := time.Now().UTC()
	return &lifecycle.ContainerView{
		ExternalID:   externalID,
		State:        "stopped",
		Profile:      "prod-strict",
		ImageVersion: "v1",
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

func (f fakeContainerService) StartContainer(_ context.Context, externalID string) (*lifecycle.ContainerView, error) {
	now := time.Now().UTC()
	return &lifecycle.ContainerView{
		ExternalID:   externalID,
		State:        lifecycle.StateRunning,
		Profile:      "prod-strict",
		ImageVersion: "v1",
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

func (f fakeContainerService) PruneContainer(_ context.Context, externalID string) (*lifecycle.ContainerView, error) {
	now := time.Now().UTC()
	return &lifecycle.ContainerView{
		ExternalID:   externalID,
		State:        lifecycle.StatePruned,
		Profile:      "prod-strict",
		ImageVersion: "v1",
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

func TestContainerRouteRejectsUnauthorized(t *testing.T) {
	containerHandler := handlers.NewContainerHandler(fakeContainerService{})
	handler := NewHandler(metrics.New(), middleware.Auth(staticAuthorizer{allow: false}), containerHandler)
	server := httptest.NewServer(handler)
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/containers", nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("run request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", resp.StatusCode)
	}
}

func TestContainerRouteAllowsAuthorized(t *testing.T) {
	containerHandler := handlers.NewContainerHandler(fakeContainerService{})
	handler := NewHandler(metrics.New(), middleware.Auth(staticAuthorizer{allow: true}), containerHandler)
	server := httptest.NewServer(handler)
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/containers", nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer any-value")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("run request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected status 201, got %d", resp.StatusCode)
	}

	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["id"] != "test-container" {
		t.Fatalf("expected container id test-container, got %v", payload["id"])
	}
}

type providerFailingService struct{}

func (providerFailingService) CreateContainer(_ context.Context) (*lifecycle.ContainerView, error) {
	return nil, fmt.Errorf("%w: create failed", lifecycle.ErrProviderFailure)
}
func (providerFailingService) GetContainer(_ context.Context, _ string) (*lifecycle.ContainerView, error) {
	return nil, fmt.Errorf("%w: get failed", lifecycle.ErrProviderFailure)
}
func (providerFailingService) StopContainer(_ context.Context, _ string) (*lifecycle.ContainerView, error) {
	return nil, fmt.Errorf("%w: stop failed", lifecycle.ErrProviderFailure)
}
func (providerFailingService) StartContainer(_ context.Context, _ string) (*lifecycle.ContainerView, error) {
	return nil, fmt.Errorf("%w: start failed", lifecycle.ErrProviderFailure)
}
func (providerFailingService) PruneContainer(_ context.Context, _ string) (*lifecycle.ContainerView, error) {
	return nil, fmt.Errorf("%w: prune failed", lifecycle.ErrProviderFailure)
}

func TestContainerRouteProviderFailureMapsToServiceUnavailable(t *testing.T) {
	containerHandler := handlers.NewContainerHandler(providerFailingService{})
	handler := NewHandler(metrics.New(), middleware.Auth(staticAuthorizer{allow: true}), containerHandler)
	server := httptest.NewServer(handler)
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/containers", nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer any-value")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("run request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d", resp.StatusCode)
	}

	var payload apierrors.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Code != "provider_unavailable" {
		t.Fatalf("expected code provider_unavailable, got %s", payload.Code)
	}
	if !payload.Retryable {
		t.Fatal("expected retryable true for provider_unavailable")
	}
}
