package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Blue-Door-Software-Group/containers4agents/internal/policy"
)

type Config struct {
	HTTPListenAddr               string
	LogLevel                     string
	DevMode                      bool
	APIKey                       string
	DBDriver                     string
	DBDSN                        string
	AutoMigrate                  bool
	StartupTimeout               time.Duration
	ShutdownTimeout              time.Duration
	DefaultProfile               string
	ActiveImageVersion           string
	DockerImage                  string
	DockerHost                   string
	DockerImagePullPolicy        string
	DockerRegistryServer         string
	DockerRegistryUsername       string
	DockerRegistryPassword       string
	ResourcePolicyFile           string
	ActiveExecLimit              int
	RunningContainerLimit        int
	TotalContainerLimit          int
	ContainerCPUMaxCores         float64
	ContainerMemoryMaxBytes      int64
	ContainerTmpDiskMaxBytes     int64
	ContainerPIDsMax             int
	ContainerExecUser            string
	ContainerDropAllCapabilities bool
	ContainerNoNewPrivileges     bool
	ContainerReadOnlyRootFS      bool
	ContainerSeccompProfilePath  string
	ContainerFileCountMax        int64
	ReconcileEnabled             bool
	ReconcileDistributed         bool
	ReconcileInterval            time.Duration
	ReconcileLockKey             int64
	ReconcileFollowerRetry       time.Duration
	IdleStopTimeout              time.Duration
	StoppedRetentionTimeout      time.Duration
	IdempotencyTTL               time.Duration
	ExecTimeout                  time.Duration
	ExecMaxStdoutBytes           int
	ExecMaxStderrBytes           int
	FileMaxUploadBytes           int
	FileMaxDownloadBytes         int
	WorkspaceDiskQuotaBytes      int64
	ReadTimeout                  time.Duration
	WriteTimeout                 time.Duration
	IdleTimeout                  time.Duration
}

func Load() (Config, error) {
	defaultPolicy := policy.Defaults()
	defaultProfileName := defaultPolicy.ActiveProfile
	defaultProfile, ok := defaultPolicy.Profiles[defaultProfileName]
	if !ok {
		return Config{}, fmt.Errorf("default policy missing active profile %q", defaultProfileName)
	}

	devMode, err := getEnvBool("DEV_MODE", false)
	if err != nil {
		return Config{}, err
	}
	autoMigrate, err := getEnvBool("AUTO_MIGRATE", false)
	if err != nil {
		return Config{}, err
	}
	reconcileEnabled, err := getEnvBool("C4A_RECONCILE_ENABLED", true)
	if err != nil {
		return Config{}, err
	}
	reconcileDistributed, err := getEnvBool("C4A_RECONCILE_DISTRIBUTED", true)
	if err != nil {
		return Config{}, err
	}
	startupTimeout, err := getEnvDuration("C4A_STARTUP_TIMEOUT", 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	shutdownTimeout, err := getEnvDuration("C4A_SHUTDOWN_TIMEOUT", 15*time.Second)
	if err != nil {
		return Config{}, err
	}
	reconcileInterval, err := getEnvDuration("C4A_RECONCILE_INTERVAL", 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	reconcileFollowerRetry, err := getEnvDuration("C4A_RECONCILE_FOLLOWER_RETRY", 5*time.Second)
	if err != nil {
		return Config{}, err
	}
	reconcileLockKey, err := getEnvInt64("C4A_RECONCILE_LOCK_KEY", 64001)
	if err != nil {
		return Config{}, err
	}
	idleStopTimeout, err := getEnvDuration("C4A_IDLE_STOP_TIMEOUT", 20*time.Minute)
	if err != nil {
		return Config{}, err
	}
	stoppedRetentionTimeout, err := getEnvDuration("C4A_STOPPED_RETENTION_TIMEOUT", 24*time.Hour)
	if err != nil {
		return Config{}, err
	}
	idempotencyTTL, err := getEnvDuration("C4A_IDEMPOTENCY_TTL", 24*time.Hour)
	if err != nil {
		return Config{}, err
	}
	execTimeout, err := getEnvDuration("C4A_EXEC_TIMEOUT", time.Duration(defaultProfile.Exec.TimeoutDefaultSeconds)*time.Second)
	if err != nil {
		return Config{}, err
	}
	execMaxStdoutBytes, err := getEnvInt("C4A_EXEC_MAX_STDOUT_BYTES", defaultProfile.Exec.StdoutMaxBytes)
	if err != nil {
		return Config{}, err
	}
	execMaxStderrBytes, err := getEnvInt("C4A_EXEC_MAX_STDERR_BYTES", defaultProfile.Exec.StderrMaxBytes)
	if err != nil {
		return Config{}, err
	}
	fileMaxUploadBytes, err := getEnvInt("C4A_FILE_MAX_UPLOAD_BYTES", defaultProfile.Files.MaxUploadBytes)
	if err != nil {
		return Config{}, err
	}
	fileMaxDownloadBytes, err := getEnvInt("C4A_FILE_MAX_DOWNLOAD_BYTES", defaultProfile.Files.MaxDownloadBytes)
	if err != nil {
		return Config{}, err
	}
	workspaceDiskQuotaBytes, err := getEnvInt64("C4A_WORKSPACE_DISK_QUOTA_BYTES", defaultProfile.Disk.WorkspaceMaxBytes)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		HTTPListenAddr:               getEnvOrDefault("C4A_HTTP_LISTEN_ADDR", ":8080"),
		LogLevel:                     strings.ToLower(getEnvOrDefault("C4A_LOG_LEVEL", "info")),
		DevMode:                      devMode,
		APIKey:                       strings.TrimSpace(os.Getenv("CONTAINERS4AGENTS_API_KEY")),
		DBDriver:                     strings.ToLower(getEnvOrDefault("C4A_DB_DRIVER", "postgres")),
		DBDSN:                        strings.TrimSpace(os.Getenv("C4A_DB_DSN")),
		AutoMigrate:                  autoMigrate,
		StartupTimeout:               startupTimeout,
		ShutdownTimeout:              shutdownTimeout,
		DefaultProfile:               strings.TrimSpace(getEnvOrDefault("C4A_DEFAULT_PROFILE", defaultProfileName)),
		ActiveImageVersion:           strings.TrimSpace(getEnvOrDefault("C4A_ACTIVE_IMAGE_VERSION", "v1")),
		DockerImage:                  strings.TrimSpace(getEnvOrDefault("C4A_DOCKER_IMAGE", "alpine:3.20")),
		DockerHost:                   strings.TrimSpace(os.Getenv("C4A_DOCKER_HOST")),
		DockerImagePullPolicy:        strings.ToLower(strings.TrimSpace(getEnvOrDefault("C4A_DOCKER_IMAGE_PULL_POLICY", "if-not-present"))),
		DockerRegistryServer:         strings.TrimSpace(getEnvOrDefault("C4A_DOCKER_REGISTRY_SERVER", "ghcr.io")),
		DockerRegistryUsername:       strings.TrimSpace(os.Getenv("C4A_DOCKER_REGISTRY_USERNAME")),
		DockerRegistryPassword:       strings.TrimSpace(os.Getenv("C4A_DOCKER_REGISTRY_PASSWORD")),
		ResourcePolicyFile:           strings.TrimSpace(os.Getenv("C4A_RESOURCE_POLICY_FILE")),
		ActiveExecLimit:              defaultPolicy.Capacity.ActiveExecLimit,
		RunningContainerLimit:        defaultPolicy.Capacity.RunningContainerLimit,
		TotalContainerLimit:          defaultPolicy.Capacity.TotalContainerLimit,
		ContainerCPUMaxCores:         defaultProfile.CPU.MaxCores,
		ContainerMemoryMaxBytes:      defaultProfile.Memory.MaxBytes,
		ContainerTmpDiskMaxBytes:     defaultProfile.Disk.TmpMaxBytes,
		ContainerPIDsMax:             defaultProfile.Process.PIDsMax,
		ContainerExecUser:            defaultProfile.Exec.User,
		ContainerDropAllCapabilities: defaultProfile.Security.DropAllCapabilities,
		ContainerNoNewPrivileges:     defaultProfile.Security.NoNewPrivileges,
		ContainerReadOnlyRootFS:      defaultProfile.Security.ReadOnlyRootFS,
		ContainerSeccompProfilePath:  strings.TrimSpace(os.Getenv("C4A_SECCOMP_PROFILE_PATH")),
		ContainerFileCountMax:        defaultProfile.Disk.FileCountMax,
		ReconcileEnabled:             reconcileEnabled,
		ReconcileDistributed:         reconcileDistributed,
		ReconcileInterval:            reconcileInterval,
		ReconcileLockKey:             reconcileLockKey,
		ReconcileFollowerRetry:       reconcileFollowerRetry,
		IdleStopTimeout:              idleStopTimeout,
		StoppedRetentionTimeout:      stoppedRetentionTimeout,
		IdempotencyTTL:               idempotencyTTL,
		ExecTimeout:                  execTimeout,
		ExecMaxStdoutBytes:           execMaxStdoutBytes,
		ExecMaxStderrBytes:           execMaxStderrBytes,
		FileMaxUploadBytes:           fileMaxUploadBytes,
		FileMaxDownloadBytes:         fileMaxDownloadBytes,
		WorkspaceDiskQuotaBytes:      workspaceDiskQuotaBytes,
		ReadTimeout:                  10 * time.Second,
		WriteTimeout:                 30 * time.Second,
		IdleTimeout:                  60 * time.Second,
	}

	if cfg.HTTPListenAddr == "" {
		return Config{}, fmt.Errorf("C4A_HTTP_LISTEN_ADDR must not be empty")
	}
	if cfg.DBDriver != "postgres" {
		return Config{}, fmt.Errorf("unsupported C4A_DB_DRIVER %q: only postgres is supported in phase 0", cfg.DBDriver)
	}
	if cfg.DBDSN == "" {
		return Config{}, fmt.Errorf("C4A_DB_DSN is required")
	}
	if cfg.DefaultProfile == "" {
		return Config{}, fmt.Errorf("C4A_DEFAULT_PROFILE must not be empty")
	}
	if cfg.ActiveImageVersion == "" {
		return Config{}, fmt.Errorf("C4A_ACTIVE_IMAGE_VERSION must not be empty")
	}
	if cfg.DockerImage == "" {
		return Config{}, fmt.Errorf("C4A_DOCKER_IMAGE must not be empty")
	}
	switch cfg.DockerImagePullPolicy {
	case "always", "if-not-present", "never":
	default:
		return Config{}, fmt.Errorf("invalid C4A_DOCKER_IMAGE_PULL_POLICY %q (allowed: always, if-not-present, never)", cfg.DockerImagePullPolicy)
	}
	if cfg.DockerRegistryUsername != "" && cfg.DockerRegistryPassword == "" {
		return Config{}, fmt.Errorf("C4A_DOCKER_REGISTRY_PASSWORD is required when C4A_DOCKER_REGISTRY_USERNAME is set")
	}
	if cfg.DockerRegistryPassword != "" && cfg.DockerRegistryUsername == "" {
		return Config{}, fmt.Errorf("C4A_DOCKER_REGISTRY_USERNAME is required when C4A_DOCKER_REGISTRY_PASSWORD is set")
	}
	if cfg.DockerRegistryServer == "" {
		return Config{}, fmt.Errorf("C4A_DOCKER_REGISTRY_SERVER must not be empty")
	}
	if !cfg.DevMode && cfg.APIKey == "" {
		return Config{}, fmt.Errorf("CONTAINERS4AGENTS_API_KEY is required when DEV_MODE is false")
	}

	switch cfg.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return Config{}, fmt.Errorf("invalid C4A_LOG_LEVEL %q", cfg.LogLevel)
	}
	if cfg.StartupTimeout <= 0 {
		return Config{}, fmt.Errorf("C4A_STARTUP_TIMEOUT must be greater than zero")
	}
	if cfg.ShutdownTimeout <= 0 {
		return Config{}, fmt.Errorf("C4A_SHUTDOWN_TIMEOUT must be greater than zero")
	}
	if cfg.ExecTimeout <= 0 {
		return Config{}, fmt.Errorf("C4A_EXEC_TIMEOUT must be greater than zero")
	}
	if cfg.ExecMaxStdoutBytes <= 0 {
		return Config{}, fmt.Errorf("C4A_EXEC_MAX_STDOUT_BYTES must be greater than zero")
	}
	if cfg.ExecMaxStderrBytes <= 0 {
		return Config{}, fmt.Errorf("C4A_EXEC_MAX_STDERR_BYTES must be greater than zero")
	}
	if cfg.FileMaxUploadBytes <= 0 {
		return Config{}, fmt.Errorf("C4A_FILE_MAX_UPLOAD_BYTES must be greater than zero")
	}
	if cfg.FileMaxDownloadBytes <= 0 {
		return Config{}, fmt.Errorf("C4A_FILE_MAX_DOWNLOAD_BYTES must be greater than zero")
	}
	if cfg.WorkspaceDiskQuotaBytes <= 0 {
		return Config{}, fmt.Errorf("C4A_WORKSPACE_DISK_QUOTA_BYTES must be greater than zero")
	}
	if cfg.ActiveExecLimit <= 0 || cfg.RunningContainerLimit <= 0 || cfg.TotalContainerLimit <= 0 {
		return Config{}, fmt.Errorf("capacity limits must be greater than zero")
	}
	if cfg.ActiveExecLimit > cfg.RunningContainerLimit || cfg.RunningContainerLimit > cfg.TotalContainerLimit {
		return Config{}, fmt.Errorf("capacity limits must satisfy active <= running <= total")
	}
	if cfg.ContainerCPUMaxCores <= 0 || cfg.ContainerMemoryMaxBytes <= 0 || cfg.ContainerTmpDiskMaxBytes <= 0 || cfg.ContainerPIDsMax <= 0 || cfg.ContainerFileCountMax <= 0 {
		return Config{}, fmt.Errorf("container runtime defaults must be greater than zero")
	}
	if cfg.ReconcileInterval <= 0 {
		return Config{}, fmt.Errorf("C4A_RECONCILE_INTERVAL must be greater than zero")
	}
	if cfg.ReconcileFollowerRetry <= 0 {
		return Config{}, fmt.Errorf("C4A_RECONCILE_FOLLOWER_RETRY must be greater than zero")
	}
	if cfg.IdleStopTimeout <= 0 {
		return Config{}, fmt.Errorf("C4A_IDLE_STOP_TIMEOUT must be greater than zero")
	}
	if cfg.StoppedRetentionTimeout <= 0 {
		return Config{}, fmt.Errorf("C4A_STOPPED_RETENTION_TIMEOUT must be greater than zero")
	}
	if cfg.IdempotencyTTL <= 0 {
		return Config{}, fmt.Errorf("C4A_IDEMPOTENCY_TTL must be greater than zero")
	}

	return cfg, nil
}

func getEnvOrDefault(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}

func getEnvBool(key string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be a valid boolean value", key)
	}
	return parsed, nil
}

func getEnvDuration(key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid duration", key)
	}
	return parsed, nil
}

func getEnvInt(key string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid integer", key)
	}
	return parsed, nil
}

func getEnvInt64(key string, fallback int64) (int64, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid integer", key)
	}
	return parsed, nil
}
