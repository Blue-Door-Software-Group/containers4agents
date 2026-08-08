package config

import "testing"

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
