package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
