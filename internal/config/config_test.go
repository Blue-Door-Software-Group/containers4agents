package config

import (
	"testing"
	"time"

	"github.com/Blue-Door-Software-Group/containers4agents/internal/policy"
)

func TestLoadRequiresDSN(t *testing.T) {
	t.Setenv("CONTAINERS4AGENTS_API_KEY", "test-key")
	t.Setenv("C4A_DB_DSN", "")
	t.Setenv("C4A_DB_DRIVER", "postgres")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error when C4A_DB_DSN is missing")
	}
}

func TestLoadRejectsUnsupportedDriver(t *testing.T) {
	t.Setenv("CONTAINERS4AGENTS_API_KEY", "test-key")
	t.Setenv("C4A_DB_DSN", "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable")
	t.Setenv("C4A_DB_DRIVER", "mysql")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for unsupported driver")
	}
}

func TestLoadSuccess(t *testing.T) {
	t.Setenv("CONTAINERS4AGENTS_API_KEY", "test-key")
	t.Setenv("C4A_DB_DSN", "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable")
	t.Setenv("C4A_DB_DRIVER", "postgres")
	t.Setenv("C4A_HTTP_LISTEN_ADDR", ":8181")
	t.Setenv("C4A_LOG_LEVEL", "debug")
	t.Setenv("AUTO_MIGRATE", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("expected load to succeed, got error: %v", err)
	}

	if cfg.HTTPListenAddr != ":8181" {
		t.Fatalf("expected listen addr :8181, got %s", cfg.HTTPListenAddr)
	}
	if !cfg.AutoMigrate {
		t.Fatal("expected auto migrate true")
	}
	if cfg.ExecTimeout != 120*time.Second {
		t.Fatalf("expected default exec timeout 120s, got %s", cfg.ExecTimeout)
	}
	if cfg.ExecMaxStdoutBytes != 4*1024*1024 {
		t.Fatalf("expected default stdout cap 4194304, got %d", cfg.ExecMaxStdoutBytes)
	}
	if cfg.ExecMaxStderrBytes != 4*1024*1024 {
		t.Fatalf("expected default stderr cap 4194304, got %d", cfg.ExecMaxStderrBytes)
	}
	if cfg.FileMaxUploadBytes != 64*1024*1024 {
		t.Fatalf("expected default upload cap 67108864, got %d", cfg.FileMaxUploadBytes)
	}
	if cfg.FileMaxDownloadBytes != 64*1024*1024 {
		t.Fatalf("expected default download cap 67108864, got %d", cfg.FileMaxDownloadBytes)
	}
	if cfg.WorkspaceDiskQuotaBytes != 10*1024*1024*1024 {
		t.Fatalf("expected default workspace quota 10737418240, got %d", cfg.WorkspaceDiskQuotaBytes)
	}
}

func TestLoadUsesPolicyDefaultsForCapacityAndRuntime(t *testing.T) {
	t.Setenv("CONTAINERS4AGENTS_API_KEY", "test-key")
	t.Setenv("C4A_DB_DSN", "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable")
	t.Setenv("C4A_DB_DRIVER", "postgres")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("expected load to succeed, got error: %v", err)
	}

	defaultPolicy := policy.Defaults()
	defaultProfile := defaultPolicy.Profiles[defaultPolicy.ActiveProfile]

	if cfg.ActiveExecLimit != defaultPolicy.Capacity.ActiveExecLimit {
		t.Fatalf("expected active exec limit %d, got %d", defaultPolicy.Capacity.ActiveExecLimit, cfg.ActiveExecLimit)
	}
	if cfg.RunningContainerLimit != defaultPolicy.Capacity.RunningContainerLimit {
		t.Fatalf("expected running container limit %d, got %d", defaultPolicy.Capacity.RunningContainerLimit, cfg.RunningContainerLimit)
	}
	if cfg.TotalContainerLimit != defaultPolicy.Capacity.TotalContainerLimit {
		t.Fatalf("expected total container limit %d, got %d", defaultPolicy.Capacity.TotalContainerLimit, cfg.TotalContainerLimit)
	}
	if cfg.ContainerCPUMaxCores != defaultProfile.CPU.MaxCores {
		t.Fatalf("expected cpu max cores %f, got %f", defaultProfile.CPU.MaxCores, cfg.ContainerCPUMaxCores)
	}
	if cfg.ContainerMemoryMaxBytes != defaultProfile.Memory.MaxBytes {
		t.Fatalf("expected memory max bytes %d, got %d", defaultProfile.Memory.MaxBytes, cfg.ContainerMemoryMaxBytes)
	}
	if cfg.ContainerFileCountMax != defaultProfile.Disk.FileCountMax {
		t.Fatalf("expected file count max %d, got %d", defaultProfile.Disk.FileCountMax, cfg.ContainerFileCountMax)
	}
}

func TestLoadRejectsInvalidAutoMigrate(t *testing.T) {
	t.Setenv("CONTAINERS4AGENTS_API_KEY", "test-key")
	t.Setenv("C4A_DB_DSN", "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable")
	t.Setenv("C4A_DB_DRIVER", "postgres")
	t.Setenv("AUTO_MIGRATE", "not-a-bool")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid AUTO_MIGRATE")
	}
}

func TestLoadRejectsInvalidStartupTimeout(t *testing.T) {
	t.Setenv("CONTAINERS4AGENTS_API_KEY", "test-key")
	t.Setenv("C4A_DB_DSN", "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable")
	t.Setenv("C4A_DB_DRIVER", "postgres")
	t.Setenv("C4A_STARTUP_TIMEOUT", "not-a-duration")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid C4A_STARTUP_TIMEOUT")
	}
}

func TestLoadRequiresAPIKeyWhenDevModeFalse(t *testing.T) {
	t.Setenv("CONTAINERS4AGENTS_API_KEY", "")
	t.Setenv("DEV_MODE", "false")
	t.Setenv("C4A_DB_DSN", "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable")
	t.Setenv("C4A_DB_DRIVER", "postgres")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error when API key is missing and DEV_MODE is false")
	}
}

func TestLoadAllowsMissingAPIKeyWhenDevModeTrue(t *testing.T) {
	t.Setenv("CONTAINERS4AGENTS_API_KEY", "")
	t.Setenv("DEV_MODE", "true")
	t.Setenv("C4A_DB_DSN", "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable")
	t.Setenv("C4A_DB_DRIVER", "postgres")

	_, err := Load()
	if err != nil {
		t.Fatalf("expected success when DEV_MODE is true, got: %v", err)
	}
}

func TestLoadRejectsInvalidExecTimeout(t *testing.T) {
	t.Setenv("CONTAINERS4AGENTS_API_KEY", "test-key")
	t.Setenv("C4A_DB_DSN", "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable")
	t.Setenv("C4A_DB_DRIVER", "postgres")
	t.Setenv("C4A_EXEC_TIMEOUT", "bad-timeout")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid C4A_EXEC_TIMEOUT")
	}
}

func TestLoadRejectsInvalidExecStdoutCap(t *testing.T) {
	t.Setenv("CONTAINERS4AGENTS_API_KEY", "test-key")
	t.Setenv("C4A_DB_DSN", "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable")
	t.Setenv("C4A_DB_DRIVER", "postgres")
	t.Setenv("C4A_EXEC_MAX_STDOUT_BYTES", "bad-int")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid C4A_EXEC_MAX_STDOUT_BYTES")
	}
}

func TestLoadRejectsNonPositiveExecCaps(t *testing.T) {
	t.Setenv("CONTAINERS4AGENTS_API_KEY", "test-key")
	t.Setenv("C4A_DB_DSN", "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable")
	t.Setenv("C4A_DB_DRIVER", "postgres")
	t.Setenv("C4A_EXEC_MAX_STDOUT_BYTES", "0")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error when C4A_EXEC_MAX_STDOUT_BYTES is not positive")
	}
}

func TestLoadRejectsInvalidFileCaps(t *testing.T) {
	t.Setenv("CONTAINERS4AGENTS_API_KEY", "test-key")
	t.Setenv("C4A_DB_DSN", "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable")
	t.Setenv("C4A_DB_DRIVER", "postgres")
	t.Setenv("C4A_FILE_MAX_UPLOAD_BYTES", "bad-int")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid C4A_FILE_MAX_UPLOAD_BYTES")
	}
}

func TestLoadRejectsNonPositiveWorkspaceQuota(t *testing.T) {
	t.Setenv("CONTAINERS4AGENTS_API_KEY", "test-key")
	t.Setenv("C4A_DB_DSN", "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable")
	t.Setenv("C4A_DB_DRIVER", "postgres")
	t.Setenv("C4A_WORKSPACE_DISK_QUOTA_BYTES", "0")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error when C4A_WORKSPACE_DISK_QUOTA_BYTES is not positive")
	}
}
