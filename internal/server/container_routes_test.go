package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	apierrors "github.com/Blue-Door-Software-Group/containers4agents/internal/api/errors"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/api/handlers"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/api/middleware"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/execution"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/files"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/lifecycle"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/observability/metrics"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/provider/types"
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
	handler := NewHandler(metrics.New(), middleware.Auth(staticAuthorizer{allow: false}), containerHandler, nil, nil)
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
	handler := NewHandler(metrics.New(), middleware.Auth(staticAuthorizer{allow: true}), containerHandler, nil, nil)
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
	handler := NewHandler(metrics.New(), middleware.Auth(staticAuthorizer{allow: true}), containerHandler, nil, nil)
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

type fakeExecutionService struct {
	result *execution.Result
	err    error
}

func (f fakeExecutionService) Execute(_ context.Context, externalID string, command []string) (*execution.Result, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.result != nil {
		return f.result, nil
	}
	return &execution.Result{
		ContainerID: externalID,
		Status:      execution.StatusOK,
		Stdout:      strings.Join(command, " "),
	}, nil
}

func TestExecRouteReturnsStructuredSuccess(t *testing.T) {
	execHandler := handlers.NewExecutionHandler(fakeExecutionService{
		result: &execution.Result{
			ContainerID:    "test-container",
			Status:         execution.StatusUserError,
			Stdout:         "",
			Stderr:         "not found",
			TruncatedOut:   false,
			TruncatedErr:   true,
			Duration:       25 * time.Millisecond,
			AppliedTimeout: 2 * time.Second,
		},
	})
	handler := NewHandler(metrics.New(), middleware.Auth(staticAuthorizer{allow: true}), nil, execHandler, nil)
	server := httptest.NewServer(handler)
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/containers/test-container/exec", strings.NewReader(`{"command":["does-not-exist"]}`))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("run request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["status"] != execution.StatusUserError {
		t.Fatalf("expected status %q, got %v", execution.StatusUserError, payload["status"])
	}
	if payload["truncated_stderr"] != true {
		t.Fatalf("expected truncated_stderr true, got %v", payload["truncated_stderr"])
	}
}

func TestExecRouteMapsConflict(t *testing.T) {
	execHandler := handlers.NewExecutionHandler(fakeExecutionService{
		err: fmt.Errorf("%w: already running", execution.ErrStateConflict),
	})
	handler := NewHandler(metrics.New(), middleware.Auth(staticAuthorizer{allow: true}), nil, execHandler, nil)
	server := httptest.NewServer(handler)
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/containers/test-container/exec", strings.NewReader(`{"command":["echo","hi"]}`))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("run request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected status 409, got %d", resp.StatusCode)
	}
}

type fakeFileService struct {
	uploadFn   func(context.Context, string, string, []byte) error
	downloadFn func(context.Context, string, string) ([]byte, error)
	deleteFn   func(context.Context, string, string) error
	manifestFn func(context.Context, string) (*files.Manifest, error)
}

func (f fakeFileService) Upload(ctx context.Context, externalID string, requestedPath string, content []byte) error {
	return f.uploadFn(ctx, externalID, requestedPath, content)
}
func (f fakeFileService) Download(ctx context.Context, externalID string, requestedPath string) ([]byte, error) {
	return f.downloadFn(ctx, externalID, requestedPath)
}
func (f fakeFileService) Delete(ctx context.Context, externalID string, requestedPath string) error {
	return f.deleteFn(ctx, externalID, requestedPath)
}
func (f fakeFileService) Manifest(ctx context.Context, externalID string) (*files.Manifest, error) {
	return f.manifestFn(ctx, externalID)
}

func TestFileRouteUploadAndDownload(t *testing.T) {
	fileHandler := handlers.NewFileHandler(fakeFileService{
		uploadFn: func(_ context.Context, _ string, path string, content []byte) error {
			if path != "dir/a.txt" {
				t.Fatalf("expected requested path dir/a.txt, got %q", path)
			}
			if string(content) != "hello" {
				t.Fatalf("expected upload payload hello, got %q", string(content))
			}
			return nil
		},
		downloadFn: func(_ context.Context, _ string, path string) ([]byte, error) {
			if path != "dir/a.txt" {
				t.Fatalf("expected requested path dir/a.txt, got %q", path)
			}
			return []byte("hello"), nil
		},
		deleteFn: func(context.Context, string, string) error { return nil },
		manifestFn: func(context.Context, string) (*files.Manifest, error) {
			return nil, nil
		},
	}, 16)

	handler := NewHandler(metrics.New(), middleware.Auth(staticAuthorizer{allow: true}), nil, nil, fileHandler)
	server := httptest.NewServer(handler)
	defer server.Close()

	uploadReq, _ := http.NewRequest(http.MethodPut, server.URL+"/v1/containers/c1/files/dir/a.txt", strings.NewReader("hello"))
	uploadReq.Header.Set("Authorization", "Bearer token")
	uploadResp, err := http.DefaultClient.Do(uploadReq)
	if err != nil {
		t.Fatalf("upload request failed: %v", err)
	}
	_ = uploadResp.Body.Close()
	if uploadResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected upload 201, got %d", uploadResp.StatusCode)
	}

	downloadReq, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/containers/c1/files/dir/a.txt", nil)
	downloadReq.Header.Set("Authorization", "Bearer token")
	downloadResp, err := http.DefaultClient.Do(downloadReq)
	if err != nil {
		t.Fatalf("download request failed: %v", err)
	}
	defer downloadResp.Body.Close()
	if downloadResp.StatusCode != http.StatusOK {
		t.Fatalf("expected download 200, got %d", downloadResp.StatusCode)
	}
	content, err := io.ReadAll(downloadResp.Body)
	if err != nil {
		t.Fatalf("read download body: %v", err)
	}
	if string(content) != "hello" {
		t.Fatalf("expected download content hello, got %q", string(content))
	}
}

func TestFileRouteManifestAndErrors(t *testing.T) {
	fileHandler := handlers.NewFileHandler(fakeFileService{
		uploadFn: func(context.Context, string, string, []byte) error { return nil },
		downloadFn: func(_ context.Context, _ string, _ string) ([]byte, error) {
			return nil, files.ErrFileNotFound
		},
		deleteFn: func(context.Context, string, string) error { return nil },
		manifestFn: func(_ context.Context, externalID string) (*files.Manifest, error) {
			if externalID != "c1" {
				t.Fatalf("expected container id c1, got %q", externalID)
			}
			return &files.Manifest{
				ContainerID:        "c1",
				WorkspaceBytesUsed: 12,
				WorkspaceDiskQuota: 1024,
				WorkspaceFileCount: 1,
				Entries: []types.FileEntry{
					{
						Path:      "dir/a.txt",
						Name:      "a.txt",
						Type:      "file",
						SizeBytes: 12,
						ModTime:   time.Unix(100, 0).UTC(),
						Hash:      &types.FileHash{Algorithm: "sha256", Value: "abc"},
					},
				},
			}, nil
		},
	}, 8)

	handler := NewHandler(metrics.New(), middleware.Auth(staticAuthorizer{allow: true}), nil, nil, fileHandler)
	server := httptest.NewServer(handler)
	defer server.Close()

	manifestReq, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/containers/c1/files/manifest", nil)
	manifestReq.Header.Set("Authorization", "Bearer token")
	manifestResp, err := http.DefaultClient.Do(manifestReq)
	if err != nil {
		t.Fatalf("manifest request failed: %v", err)
	}
	defer manifestResp.Body.Close()
	if manifestResp.StatusCode != http.StatusOK {
		t.Fatalf("expected manifest 200, got %d", manifestResp.StatusCode)
	}

	var manifestPayload map[string]any
	if err := json.NewDecoder(manifestResp.Body).Decode(&manifestPayload); err != nil {
		t.Fatalf("decode manifest response: %v", err)
	}
	if manifestPayload["workspace_file_count"] != float64(1) {
		t.Fatalf("expected workspace_file_count 1, got %v", manifestPayload["workspace_file_count"])
	}

	downloadReq, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/containers/c1/files/missing.txt", nil)
	downloadReq.Header.Set("Authorization", "Bearer token")
	downloadResp, err := http.DefaultClient.Do(downloadReq)
	if err != nil {
		t.Fatalf("download request failed: %v", err)
	}
	defer downloadResp.Body.Close()
	if downloadResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected missing file 404, got %d", downloadResp.StatusCode)
	}
}
