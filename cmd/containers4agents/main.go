package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Blue-Door-Software-Group/containers4agents/internal/api/handlers"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/api/middleware"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/auth"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/config"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/execution"
	filesvc "github.com/Blue-Door-Software-Group/containers4agents/internal/files"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/idempotency"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/lifecycle"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/metadata/entrepo"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/metadata/migrations"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/metadata/repository"
	obslog "github.com/Blue-Door-Software-Group/containers4agents/internal/observability/logging"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/observability/metrics"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/policy"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/provider/docker"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/reconcile"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/server"
)

func main() {
	if err := run(); err != nil {
		slog.Error("service exited with error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger := obslog.New(cfg.LogLevel)

	resourcePolicy, policySource, err := policy.Load(cfg.ResourcePolicyFile)
	if err != nil {
		return fmt.Errorf("load resource policy: %w", err)
	}
	if err := applyResourcePolicy(&cfg, resourcePolicy); err != nil {
		return fmt.Errorf("apply resource policy: %w", err)
	}

	logger.Info("starting containers4agents", "addr", cfg.HTTPListenAddr, "db_driver", cfg.DBDriver, "auto_migrate", cfg.AutoMigrate)
	logger.Info(
		"resource policy loaded",
		"source", policySource,
		"profile", cfg.DefaultProfile,
		"active_exec_limit", cfg.ActiveExecLimit,
		"running_container_limit", cfg.RunningContainerLimit,
		"total_container_limit", cfg.TotalContainerLimit,
	)

	repoClient, err := entrepo.New(cfg)
	if err != nil {
		return fmt.Errorf("initialize ent repository client: %w", err)
	}
	defer func() {
		if closeErr := repoClient.Close(); closeErr != nil {
			logger.Error("failed to close ent client", "error", closeErr)
		}
	}()

	startupCtx, startupCancel := context.WithTimeout(context.Background(), cfg.StartupTimeout)
	defer startupCancel()

	if err := repoClient.DB.PingContext(startupCtx); err != nil {
		return fmt.Errorf("ping postgres: %w", err)
	}

	if err := migrations.Run(startupCtx, logger, repoClient.Ent, cfg.AutoMigrate); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}

	keyManager, err := auth.NewKeyManager(cfg.APIKey, cfg.DevMode)
	if err != nil {
		return fmt.Errorf("initialize auth key manager: %w", err)
	}
	if keyManager.Generated() {
		logger.Info("generated API key for this process", "api_key", keyManager.APIKey())
	}

	dockerProvider, err := docker.New(cfg.DockerHost)
	if err != nil {
		return fmt.Errorf("initialize docker provider: %w", err)
	}
	defer func() {
		if closeErr := dockerProvider.Close(); closeErr != nil {
			logger.Error("failed to close docker provider", "error", closeErr)
		}
	}()

	containerRepo := repository.NewEntContainerRepository(repoClient.Ent)
	metricsRegistry := metrics.New()
	idempotencySvc := idempotency.NewService(containerRepo, cfg.IdempotencyTTL)
	lifecycleSvc := lifecycle.NewService(containerRepo, dockerProvider, cfg, metricsRegistry)
	executionSvc := execution.NewService(containerRepo, dockerProvider, cfg, metricsRegistry)
	fileSvc := filesvc.NewService(containerRepo, dockerProvider, cfg, metricsRegistry)
	containerHandler := handlers.NewContainerHandler(lifecycleSvc, idempotencySvc)
	executionHandler := handlers.NewExecutionHandler(executionSvc)
	fileHandler := handlers.NewFileHandler(fileSvc, cfg.FileMaxUploadBytes, idempotencySvc)
	authMw := middleware.Auth(keyManager, metricsRegistry)
	handler := server.NewHandler(metricsRegistry, authMw, containerHandler, executionHandler, fileHandler)
	apiServer := server.New(cfg, logger, handler)
	reconcileWorker := reconcile.NewWorker(
		repoClient.DB,
		containerRepo,
		lifecycleSvc,
		logger,
		cfg.ReconcileEnabled,
		cfg.ReconcileDistributed,
		cfg.ReconcileInterval,
		cfg.ReconcileFollowerRetry,
		cfg.IdleStopTimeout,
		cfg.StoppedRetentionTimeout,
		cfg.ReconcileLockKey,
		metricsRegistry,
	)

	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(signalCh)

	serverErrCh := make(chan error, 1)
	go func() {
		serverErrCh <- apiServer.Start()
	}()
	reconcileCtx, reconcileCancel := context.WithCancel(context.Background())
	defer reconcileCancel()
	reconcileDoneCh := make(chan struct{})
	go func() {
		defer close(reconcileDoneCh)
		for {
			select {
			case <-reconcileCtx.Done():
				return
			default:
			}
			if err := reconcileWorker.Run(reconcileCtx); err != nil {
				if errors.Is(err, reconcile.ErrPermanentFailure) {
					logger.Error("reconcile worker hit permanent failure; stopping worker loop", "error", err)
					return
				}
				logger.Error("reconcile worker exited with error; retrying", "error", err)
				timer := time.NewTimer(cfg.ReconcileFollowerRetry)
				select {
				case <-reconcileCtx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
				continue
			}
			return
		}
	}()

	shutdownWithTimeout := func(sig os.Signal) error {
		logger.Info("shutdown signal received", "signal", sig.String())
		reconcileCancel()
		waitForReconcileShutdown(logger, reconcileDoneCh, cfg.ShutdownTimeout)
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer shutdownCancel()

		shutdownErr := apiServer.Shutdown(shutdownCtx)
		if shutdownErr != nil {
			return fmt.Errorf("shutdown server: %w", shutdownErr)
		}

		select {
		case startErr := <-serverErrCh:
			return startErr
		case <-shutdownCtx.Done():
			return fmt.Errorf("server did not exit before shutdown timeout: %w", shutdownCtx.Err())
		}
	}

	for {
		select {
		case startErr := <-serverErrCh:
			reconcileCancel()
			waitForReconcileShutdown(logger, reconcileDoneCh, cfg.ShutdownTimeout)
			return startErr
		case sig := <-signalCh:
			return shutdownWithTimeout(sig)
		}
	}
}

func waitForReconcileShutdown(logger *slog.Logger, done <-chan struct{}, timeout time.Duration) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		logger.Warn("reconcile worker did not exit before timeout")
	}
}

func applyResourcePolicy(cfg *config.Config, resourcePolicy policy.ResourcePolicy) error {
	if cfg == nil {
		return fmt.Errorf("config must not be nil")
	}
	activeProfileName := cfg.DefaultProfile
	if activeProfileName == "" {
		activeProfileName = resourcePolicy.ActiveProfile
	}
	profile, ok := resourcePolicy.Profiles[activeProfileName]
	if !ok {
		return fmt.Errorf("profile %q not found in resource policy", activeProfileName)
	}

	cfg.DefaultProfile = activeProfileName
	cfg.ActiveExecLimit = resourcePolicy.Capacity.ActiveExecLimit
	cfg.RunningContainerLimit = resourcePolicy.Capacity.RunningContainerLimit
	cfg.TotalContainerLimit = resourcePolicy.Capacity.TotalContainerLimit
	cfg.ContainerCPUMaxCores = profile.CPU.MaxCores
	cfg.ContainerMemoryMaxBytes = profile.Memory.MaxBytes
	cfg.WorkspaceDiskQuotaBytes = profile.Disk.WorkspaceMaxBytes
	cfg.ContainerTmpDiskMaxBytes = profile.Disk.TmpMaxBytes
	cfg.ContainerFileCountMax = profile.Disk.FileCountMax
	cfg.ContainerPIDsMax = profile.Process.PIDsMax
	cfg.ContainerExecUser = profile.Exec.User
	cfg.ContainerDropAllCapabilities = profile.Security.DropAllCapabilities
	cfg.ContainerNoNewPrivileges = profile.Security.NoNewPrivileges
	cfg.ContainerReadOnlyRootFS = profile.Security.ReadOnlyRootFS
	if profile.Security.SeccompProfilePath != "" {
		cfg.ContainerSeccompProfilePath = profile.Security.SeccompProfilePath
	}
	cfg.ExecTimeout = time.Duration(profile.Exec.TimeoutDefaultSeconds) * time.Second
	cfg.ExecMaxStdoutBytes = profile.Exec.StdoutMaxBytes
	cfg.ExecMaxStderrBytes = profile.Exec.StderrMaxBytes
	cfg.FileMaxUploadBytes = profile.Files.MaxUploadBytes
	cfg.FileMaxDownloadBytes = profile.Files.MaxDownloadBytes
	return nil
}
