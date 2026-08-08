package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPListenAddr     string
	LogLevel           string
	DevMode            bool
	APIKey             string
	DBDriver           string
	DBDSN              string
	AutoMigrate        bool
	StartupTimeout     time.Duration
	ShutdownTimeout    time.Duration
	DefaultProfile     string
	ActiveImageVersion string
	DockerImage        string
	DockerHost         string
	ExecTimeout        time.Duration
	ExecMaxStdoutBytes int
	ExecMaxStderrBytes int
	ReadTimeout        time.Duration
	WriteTimeout       time.Duration
	IdleTimeout        time.Duration
}

func Load() (Config, error) {
	devMode, err := getEnvBool("DEV_MODE", false)
	if err != nil {
		return Config{}, err
	}
	autoMigrate, err := getEnvBool("AUTO_MIGRATE", false)
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
	execTimeout, err := getEnvDuration("C4A_EXEC_TIMEOUT", 120*time.Second)
	if err != nil {
		return Config{}, err
	}
	execMaxStdoutBytes, err := getEnvInt("C4A_EXEC_MAX_STDOUT_BYTES", 1024*1024)
	if err != nil {
		return Config{}, err
	}
	execMaxStderrBytes, err := getEnvInt("C4A_EXEC_MAX_STDERR_BYTES", 1024*1024)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		HTTPListenAddr:     getEnvOrDefault("C4A_HTTP_LISTEN_ADDR", ":8080"),
		LogLevel:           strings.ToLower(getEnvOrDefault("C4A_LOG_LEVEL", "info")),
		DevMode:            devMode,
		APIKey:             strings.TrimSpace(os.Getenv("CONTAINERS4AGENTS_API_KEY")),
		DBDriver:           strings.ToLower(getEnvOrDefault("C4A_DB_DRIVER", "postgres")),
		DBDSN:              strings.TrimSpace(os.Getenv("C4A_DB_DSN")),
		AutoMigrate:        autoMigrate,
		StartupTimeout:     startupTimeout,
		ShutdownTimeout:    shutdownTimeout,
		DefaultProfile:     strings.TrimSpace(getEnvOrDefault("C4A_DEFAULT_PROFILE", "prod-strict")),
		ActiveImageVersion: strings.TrimSpace(getEnvOrDefault("C4A_ACTIVE_IMAGE_VERSION", "v1")),
		DockerImage:        strings.TrimSpace(getEnvOrDefault("C4A_DOCKER_IMAGE", "alpine:3.20")),
		DockerHost:         strings.TrimSpace(os.Getenv("C4A_DOCKER_HOST")),
		ExecTimeout:        execTimeout,
		ExecMaxStdoutBytes: execMaxStdoutBytes,
		ExecMaxStderrBytes: execMaxStderrBytes,
		ReadTimeout:        10 * time.Second,
		WriteTimeout:       30 * time.Second,
		IdleTimeout:        60 * time.Second,
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
