//go:build integration

package entrepo

import (
	"context"
	"os"
	"testing"

	"github.com/Blue-Door-Software-Group/containers4agents/internal/config"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/metadata/migrations"
	obslog "github.com/Blue-Door-Software-Group/containers4agents/internal/observability/logging"
)

func TestClientConnectsAndMigrates(t *testing.T) {
	dsn := os.Getenv("C4A_TEST_DB_DSN")
	if dsn == "" {
		t.Skip("C4A_TEST_DB_DSN not set")
	}

	cfg := config.Config{
		DBDriver:    "postgres",
		DBDSN:       dsn,
		AutoMigrate: true,
	}

	repoClient, err := New(cfg)
	if err != nil {
		t.Fatalf("create ent repo client: %v", err)
	}
	defer repoClient.Close()

	if err := repoClient.DB.PingContext(context.Background()); err != nil {
		t.Fatalf("ping database: %v", err)
	}

	logger := obslog.New("info")
	if err := migrations.Run(context.Background(), logger, repoClient.Ent, true); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
}
