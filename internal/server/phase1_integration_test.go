//go:build integration

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/Blue-Door-Software-Group/containers4agents/internal/api/handlers"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/api/middleware"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/auth"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/config"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/execution"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/lifecycle"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/metadata/entrepo"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/metadata/migrations"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/metadata/repository"
	obslog "github.com/Blue-Door-Software-Group/containers4agents/internal/observability/logging"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/observability/metrics"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/provider/docker"
)

type phase1IntegrationEnv struct {
	cfg        config.Config
	repoClient *entrepo.Client
	provider   *docker.Provider
	httpServer *httptest.Server
}

func setupPhase1IntegrationEnv(t *testing.T) *phase1IntegrationEnv {
	t.Helper()

	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker binary not available")
	}

	dsn := os.Getenv("C4A_TEST_DB_DSN")
	if dsn == "" {
		t.Skip("C4A_TEST_DB_DSN not set")
	}

	cfg := config.Config{
		HTTPListenAddr:     ":0",
		LogLevel:           "info",
		APIKey:             "integration-key",
		DBDriver:           "postgres",
		DBDSN:              dsn,
		AutoMigrate:        true,
		StartupTimeout:     20 * time.Second,
		ShutdownTimeout:    10 * time.Second,
		DefaultProfile:     "prod-strict",
		ActiveImageVersion: "v1",
		DockerImage:        envOrDefault("C4A_TEST_DOCKER_IMAGE", "alpine:3.20"),
		ExecTimeout:        2 * time.Second,
		ExecMaxStdoutBytes: 4096,
		ExecMaxStderrBytes: 4096,
		ReadTimeout:        10 * time.Second,
		WriteTimeout:       30 * time.Second,
		IdleTimeout:        60 * time.Second,
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

	dockerProvider, err := docker.New(cfg.DockerHost)
	if err != nil {
		t.Fatalf("create docker provider: %v", err)
	}

	containerRepo := repository.NewEntContainerRepository(repoClient.Ent)
	lifecycleSvc := lifecycle.NewService(containerRepo, dockerProvider, cfg)
	executionSvc := execution.NewService(containerRepo, dockerProvider, cfg)
	containerHandler := handlers.NewContainerHandler(lifecycleSvc)
	executionHandler := handlers.NewExecutionHandler(executionSvc)
	keyManager, err := auth.NewKeyManager(cfg.APIKey, false)
	if err != nil {
		t.Fatalf("create key manager: %v", err)
	}

	handler := NewHandler(metrics.New(), middleware.Auth(keyManager), containerHandler, executionHandler)
	httpServer := httptest.NewServer(handler)

	return &phase1IntegrationEnv{
		cfg:        cfg,
		repoClient: repoClient,
		provider:   dockerProvider,
		httpServer: httpServer,
	}
}

func (e *phase1IntegrationEnv) close() {
	e.httpServer.Close()
	_ = e.provider.Close()
	_ = e.repoClient.Close()
}

func TestPhase1CreateStatusStopAndRestartPersistence(t *testing.T) {
	env := setupPhase1IntegrationEnv(t)
	defer env.close()

	// Unauthorized request should be rejected.
	unauthReq, _ := http.NewRequest(http.MethodPost, env.httpServer.URL+"/v1/containers", nil)
	unauthResp, err := http.DefaultClient.Do(unauthReq)
	if err != nil {
		t.Fatalf("unauthorized create request failed: %v", err)
	}
	if unauthResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized status 401, got %d", unauthResp.StatusCode)
	}
	_ = unauthResp.Body.Close()

	// Authorized create.
	createReq, _ := http.NewRequest(http.MethodPost, env.httpServer.URL+"/v1/containers", nil)
	createReq.Header.Set("Authorization", "Bearer integration-key")
	createResp, err := http.DefaultClient.Do(createReq)
	if err != nil {
		t.Fatalf("create request failed: %v", err)
	}
	defer createResp.Body.Close()
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected create status 201, got %d", createResp.StatusCode)
	}

	var created map[string]any
	if err := json.NewDecoder(createResp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	containerID, _ := created["id"].(string)
	if containerID == "" {
		t.Fatal("expected created container id")
	}

	// Status should show running.
	statusReq, _ := http.NewRequest(http.MethodGet, env.httpServer.URL+"/v1/containers/"+containerID, nil)
	statusReq.Header.Set("Authorization", "Bearer integration-key")
	statusResp, err := http.DefaultClient.Do(statusReq)
	if err != nil {
		t.Fatalf("status request failed: %v", err)
	}
	defer statusResp.Body.Close()
	if statusResp.StatusCode != http.StatusOK {
		t.Fatalf("expected status code 200, got %d", statusResp.StatusCode)
	}

	// Stop should move to stopped.
	stopReq, _ := http.NewRequest(http.MethodPost, env.httpServer.URL+"/v1/containers/"+containerID+"/stop", nil)
	stopReq.Header.Set("Authorization", "Bearer integration-key")
	stopResp, err := http.DefaultClient.Do(stopReq)
	if err != nil {
		t.Fatalf("stop request failed: %v", err)
	}
	defer stopResp.Body.Close()
	if stopResp.StatusCode != http.StatusOK {
		t.Fatalf("expected stop status 200, got %d", stopResp.StatusCode)
	}

	// Restart API process and verify persisted state.
	env.httpServer.Close()
	repo := repository.NewEntContainerRepository(env.repoClient.Ent)
	lifecycleSvc := lifecycle.NewService(repo, env.provider, env.cfg)
	executionSvc := execution.NewService(repo, env.provider, env.cfg)
	handler := NewHandler(
		metrics.New(),
		middleware.Auth(mustKeyManager(t, "integration-key")),
		handlers.NewContainerHandler(lifecycleSvc),
		handlers.NewExecutionHandler(executionSvc),
	)
	env.httpServer = httptest.NewServer(handler)

	statusReq2, _ := http.NewRequest(http.MethodGet, env.httpServer.URL+"/v1/containers/"+containerID, nil)
	statusReq2.Header.Set("Authorization", "Bearer integration-key")
	statusResp2, err := http.DefaultClient.Do(statusReq2)
	if err != nil {
		t.Fatalf("status request after restart failed: %v", err)
	}
	defer statusResp2.Body.Close()
	if statusResp2.StatusCode != http.StatusOK {
		t.Fatalf("expected status code 200 after restart, got %d", statusResp2.StatusCode)
	}

	var statusPayload map[string]any
	if err := json.NewDecoder(statusResp2.Body).Decode(&statusPayload); err != nil {
		t.Fatalf("decode status response after restart: %v", err)
	}
	if statusPayload["state"] != "stopped" {
		t.Fatalf("expected persisted state stopped after restart, got %v", statusPayload["state"])
	}
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
