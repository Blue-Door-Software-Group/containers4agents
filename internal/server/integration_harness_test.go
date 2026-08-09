//go:build integration

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Blue-Door-Software-Group/containers4agents/internal/api/handlers"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/api/middleware"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/auth"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/config"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/execution"
	filesvc "github.com/Blue-Door-Software-Group/containers4agents/internal/files"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/lifecycle"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/metadata/entrepo"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/metadata/migrations"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/metadata/repository"
	obslog "github.com/Blue-Door-Software-Group/containers4agents/internal/observability/logging"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/observability/metrics"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/provider/docker"
)

type integrationTestEnv struct {
	cfg        config.Config
	repoClient *entrepo.Client
	provider   *docker.Provider
	httpServer *httptest.Server
}

func setupIntegrationEnv(t *testing.T, tweak func(*config.Config)) *integrationTestEnv {
	t.Helper()

	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker binary not available")
	}

	dsn := os.Getenv("C4A_TEST_DB_DSN")
	if dsn == "" {
		t.Skip("C4A_TEST_DB_DSN not set")
	}

	cfg := config.Config{
		HTTPListenAddr:               ":0",
		LogLevel:                     "info",
		APIKey:                       "integration-key",
		DBDriver:                     "postgres",
		DBDSN:                        dsn,
		AutoMigrate:                  true,
		StartupTimeout:               20 * time.Second,
		ShutdownTimeout:              10 * time.Second,
		DefaultProfile:               "prod-strict",
		ActiveImageVersion:           "v1",
		DockerImage:                  envOrDefault("C4A_TEST_DOCKER_IMAGE", "alpine:3.20"),
		ActiveExecLimit:              10,
		RunningContainerLimit:        30,
		TotalContainerLimit:          100,
		ContainerCPUMaxCores:         2.0,
		ContainerMemoryMaxBytes:      3 * 1024 * 1024 * 1024,
		ContainerTmpDiskMaxBytes:     2 * 1024 * 1024 * 1024,
		ContainerPIDsMax:             256,
		ContainerExecUser:            "65534:65534",
		ContainerDropAllCapabilities: true,
		ContainerNoNewPrivileges:     true,
		ContainerReadOnlyRootFS:      false,
		ContainerFileCountMax:        100000,
		ExecTimeout:                  5 * time.Second,
		ExecMaxStdoutBytes:           4096,
		ExecMaxStderrBytes:           4096,
		FileMaxUploadBytes:           1024 * 1024,
		FileMaxDownloadBytes:         1024 * 1024,
		WorkspaceDiskQuotaBytes:      1024 * 1024 * 1024,
		ReadTimeout:                  10 * time.Second,
		WriteTimeout:                 30 * time.Second,
		IdleTimeout:                  60 * time.Second,
	}
	if tweak != nil {
		tweak(&cfg)
	}

	logger := obslog.New(cfg.LogLevel)
	repoClient, err := entrepo.New(cfg)
	if err != nil {
		t.Fatalf("create entrepo client: %v", err)
	}
	if err := repoClient.DB.PingContext(context.Background()); err != nil {
		t.Fatalf("ping database: %v", err)
	}
	if err := migrations.Run(context.Background(), logger, repoClient.Ent, true); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	dockerProvider, err := docker.NewWithOptions(cfg.DockerHost, docker.Options{
		ImagePullPolicy:  cfg.DockerImagePullPolicy,
		RegistryServer:   cfg.DockerRegistryServer,
		RegistryUsername: cfg.DockerRegistryUsername,
		RegistryPassword: cfg.DockerRegistryPassword,
		DevMode:          cfg.DevMode,
	})
	if err != nil {
		t.Fatalf("create docker provider: %v", err)
	}

	containerRepo := repository.NewEntContainerRepository(repoClient.Ent)
	lifecycleSvc := lifecycle.NewService(containerRepo, dockerProvider, cfg)
	executionSvc := execution.NewService(containerRepo, dockerProvider, cfg)
	fileSvc := filesvc.NewService(containerRepo, dockerProvider, cfg)
	containerHandler := handlers.NewContainerHandler(lifecycleSvc)
	executionHandler := handlers.NewExecutionHandler(executionSvc)
	fileHandler := handlers.NewFileHandler(fileSvc, cfg.FileMaxUploadBytes)
	keyManager, err := auth.NewKeyManager(cfg.APIKey, false)
	if err != nil {
		t.Fatalf("create key manager: %v", err)
	}

	handler := NewHandler(metrics.New(), middleware.Auth(keyManager), containerHandler, executionHandler, fileHandler)
	httpServer := httptest.NewServer(handler)

	return &integrationTestEnv{
		cfg:        cfg,
		repoClient: repoClient,
		provider:   dockerProvider,
		httpServer: httpServer,
	}
}

func (e *integrationTestEnv) close() {
	e.httpServer.Close()
	_ = e.provider.Close()
	_ = e.repoClient.Close()
}

func createContainerForExec(t *testing.T, baseURL string) string {
	t.Helper()
	createReq, _ := http.NewRequest(http.MethodPost, baseURL+"/v1/containers", nil)
	createReq.Header.Set("Authorization", "Bearer integration-key")
	createResp, err := http.DefaultClient.Do(createReq)
	if err != nil {
		t.Fatalf("create request failed: %v", err)
	}
	defer createResp.Body.Close()
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected create status 201, got %d", createResp.StatusCode)
	}

	var payload map[string]any
	if err := json.NewDecoder(createResp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	containerID, _ := payload["id"].(string)
	if containerID == "" {
		t.Fatal("expected container id")
	}
	return containerID
}

func execCommand(t *testing.T, baseURL, containerID, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/containers/"+containerID+"/exec", strings.NewReader(body))
	if err != nil {
		t.Fatalf("create exec request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer integration-key")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("run exec request: %v", err)
	}
	return resp
}

func mustKeyManager(t *testing.T, configured string) *auth.KeyManager {
	t.Helper()
	m, err := auth.NewKeyManager(configured, false)
	if err != nil {
		t.Fatalf("create key manager: %v", err)
	}
	return m
}

func envOrDefault(key, fallback string) string {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	return v
}
