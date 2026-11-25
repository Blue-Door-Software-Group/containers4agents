package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strings"
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
		"default_image", cfg.DefaultImageName,
		"images", len(cfg.Images),
	)
	warnUnguardedEgress(logger, cfg, egressRulesInstalled)

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

	dockerProvider, err := docker.NewWithOptions(cfg.DockerHost, docker.Options{
		ImagePullPolicy:  cfg.DockerImagePullPolicy,
		RegistryServer:   cfg.DockerRegistryServer,
		RegistryUsername: cfg.DockerRegistryUsername,
		RegistryPassword: cfg.DockerRegistryPassword,
		DevMode:          cfg.DevMode,
	})
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
	containerHandler := handlers.NewContainerHandler(lifecycleSvc, idempotencySvc).WithImages(imageInfos(cfg))
	executionHandler := handlers.NewExecutionHandler(executionSvc)
	fileHandler := handlers.NewFileHandler(fileSvc, maxUploadBytes(cfg), idempotencySvc)
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
	applyProfile(cfg, profile)
	return applyImages(cfg, resourcePolicy)
}

// applyProfile flattens a runtime profile into cfg.
func applyProfile(cfg *config.Config, profile policy.RuntimeProfile) {
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
}

// applyImages resolves the image registry into cfg.Images. The implicit image (C4A_DOCKER_IMAGE
// under the active profile, no network) is always there unless the registry redefines it, so a
// policy without images behaves exactly as before.
func applyImages(cfg *config.Config, resourcePolicy policy.ResourcePolicy) error {
	base := *cfg
	base.Images = nil
	images := map[string]config.Image{
		policy.ImplicitImageName: {Name: policy.ImplicitImageName, Network: policy.NetworkNone, Settings: base},
	}
	for name, spec := range resourcePolicy.Images {
		settings := base
		profileName := spec.Profile
		if profileName == "" {
			profileName = base.DefaultProfile
		}
		profile, ok := resourcePolicy.Profiles[profileName]
		if !ok {
			return fmt.Errorf("image %q: profile %q not found in resource policy", name, profileName)
		}
		settings.DefaultProfile = profileName
		applyProfile(&settings, profile)
		settings.DockerImage = os.ExpandEnv(spec.Ref)
		if settings.DockerImage == "" {
			return fmt.Errorf("image %q: ref is empty", name)
		}
		if spec.IdleStopTimeout != "" {
			d, err := time.ParseDuration(spec.IdleStopTimeout)
			if err != nil {
				return fmt.Errorf("image %q: idle_stop_timeout: %w", name, err)
			}
			settings.IdleStopTimeout = d
		}
		if spec.StoppedRetentionTimeout != "" {
			d, err := time.ParseDuration(spec.StoppedRetentionTimeout)
			if err != nil {
				return fmt.Errorf("image %q: stopped_retention_timeout: %w", name, err)
			}
			settings.StoppedRetentionTimeout = d
		}
		network := spec.Network
		if network == "" {
			network = policy.NetworkNone
		}
		images[name] = config.Image{Name: name, Network: network, Settings: settings}
	}
	cfg.Images = images
	cfg.DefaultImageName = resourcePolicy.DefaultImage
	if cfg.DefaultImageName == "" {
		cfg.DefaultImageName = policy.ImplicitImageName
	}
	if _, ok := images[cfg.DefaultImageName]; !ok {
		return fmt.Errorf("default_image %q is not a registered image", cfg.DefaultImageName)
	}
	return nil
}

// imageInfos lists the registered images for GET /v1/images, sorted by name.
func imageInfos(cfg config.Config) []handlers.ImageInfo {
	names := make([]string, 0, len(cfg.Images))
	for name := range cfg.Images {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]handlers.ImageInfo, 0, len(names))
	for _, name := range names {
		img := cfg.Images[name]
		out = append(out, handlers.ImageInfo{
			Name:                    name,
			Default:                 name == cfg.DefaultImageName,
			Profile:                 img.Settings.DefaultProfile,
			Network:                 img.Network,
			IdleStopTimeout:         img.Settings.IdleStopTimeout.String(),
			StoppedRetentionTimeout: img.Settings.StoppedRetentionTimeout.String(),
		})
	}
	return out
}

// maxUploadBytes is the largest upload any image allows: the HTTP body cap. Each container's own
// image limit is enforced by the files service.
func maxUploadBytes(cfg config.Config) int {
	limit := cfg.FileMaxUploadBytes
	for _, img := range cfg.Images {
		limit = max(limit, img.Settings.FileMaxUploadBytes)
	}
	return limit
}

// egressRulesInstalled reports whether scripts/egress-firewall.sh's DOCKER-USER rules are in place
// (ok false: they could not be checked, e.g. no iptables or not root).
func egressRulesInstalled() (installed, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// Both chains: DOCKER-USER (forwarded traffic) and INPUT (the host itself).
	for _, chain := range []string{"DOCKER-USER", "INPUT"} {
		out, err := exec.CommandContext(ctx, "iptables", "-S", chain).Output()
		if err != nil {
			return false, false
		}
		if !strings.Contains(string(out), "c4a-egress") {
			return false, true
		}
	}
	return true, true
}

// warnUnguardedEgress warns when an image has egress but the deny rules for it are not installed:
// its sandboxes could reach the metadata service, private networks and the control planes.
func warnUnguardedEgress(logger *slog.Logger, cfg config.Config, check func() (bool, bool)) {
	var egress []string
	for name, img := range cfg.Images {
		if img.Network == policy.NetworkEgress {
			egress = append(egress, name)
		}
	}
	if len(egress) == 0 {
		return
	}
	sort.Strings(egress)
	installed, ok := check()
	switch {
	case !ok:
		logger.Warn("egress images registered; could not verify the egress deny rules (run scripts/egress-firewall.sh as root on the Docker host)", "images", egress)
	case !installed:
		logger.Warn("egress images registered without the egress deny rules: their sandboxes can reach metadata, private networks and control planes; run scripts/egress-firewall.sh", "images", egress)
	}
}
