//go:build integration

package server

import (
	"bytes"
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
	filesvc "github.com/Blue-Door-Software-Group/containers4agents/internal/files"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/lifecycle"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/metadata/entrepo"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/metadata/migrations"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/metadata/repository"
	obslog "github.com/Blue-Door-Software-Group/containers4agents/internal/observability/logging"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/observability/metrics"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/provider/docker"
)

func TestPhase5SandboxViolationAndNonRootExec(t *testing.T) {
	env := setupPhase5IntegrationEnv(t, func(cfg *config.Config) {
		cfg.ContainerDropAllCapabilities = true
		cfg.ContainerNoNewPrivileges = true
		cfg.ContainerExecUser = "65534:65534"
	})
	defer env.close()

	containerID := createContainerForExec(t, env.httpServer.URL)

	uidResp := execCommand(t, env.httpServer.URL, containerID, `{"command":["sh","-lc","id -u"]}`)
	if uidResp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 for id -u, got %d", uidResp.StatusCode)
	}
	var uidPayload map[string]any
	if err := json.NewDecoder(uidResp.Body).Decode(&uidPayload); err != nil {
		t.Fatalf("decode uid payload: %v", err)
	}
	_ = uidResp.Body.Close()
	if uidPayload["stdout"] == "0\n" {
		t.Fatalf("expected non-root exec user, got stdout=%q", uidPayload["stdout"])
	}

	sandboxResp := execCommand(t, env.httpServer.URL, containerID, `{"command":["sh","-lc","mkdir -p /tmp/mnt && mount -t tmpfs none /tmp/mnt"]}`)
	if sandboxResp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 for mount probe, got %d", sandboxResp.StatusCode)
	}
	var sandboxPayload map[string]any
	if err := json.NewDecoder(sandboxResp.Body).Decode(&sandboxPayload); err != nil {
		t.Fatalf("decode sandbox payload: %v", err)
	}
	_ = sandboxResp.Body.Close()
	status, _ := sandboxPayload["status"].(string)
	if status != "sandbox_violation" && status != "user_error" {
		t.Fatalf("expected sandbox_violation or user_error, got %q", status)
	}
}

func TestPhase5WorkspaceQuotaUploadLimit(t *testing.T) {
	env := setupPhase5IntegrationEnv(t, func(cfg *config.Config) {
		cfg.WorkspaceDiskQuotaBytes = 1024
		cfg.FileMaxUploadBytes = 8192
		cfg.ContainerFileCountMax = 1000
	})
	defer env.close()

	containerID := createContainerForExec(t, env.httpServer.URL)

	body := bytes.Repeat([]byte("a"), 2048)
	uploadReq, _ := http.NewRequest(http.MethodPut, env.httpServer.URL+"/v1/containers/"+containerID+"/files/too-big.txt", bytes.NewReader(body))
	uploadReq.Header.Set("Authorization", "Bearer integration-key")
	uploadResp, err := http.DefaultClient.Do(uploadReq)
	if err != nil {
		t.Fatalf("upload request failed: %v", err)
	}
	defer uploadResp.Body.Close()
	if uploadResp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for quota overflow, got %d", uploadResp.StatusCode)
	}
}

type phase5IntegrationEnv struct {
	cfg        config.Config
	repoClient *entrepo.Client
	provider   *docker.Provider
	httpServer *httptest.Server
}

func setupPhase5IntegrationEnv(t *testing.T, tweak func(*config.Config)) *phase5IntegrationEnv {
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
	dockerProvider, err := docker.New(cfg.DockerHost)
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

	return &phase5IntegrationEnv{
		cfg:        cfg,
		repoClient: repoClient,
		provider:   dockerProvider,
		httpServer: httpServer,
	}
}

func (e *phase5IntegrationEnv) close() {
	e.httpServer.Close()
	_ = e.provider.Close()
	_ = e.repoClient.Close()
}
