//go:build integration

package repository

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/Blue-Door-Software-Group/containers4agents/internal/config"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/metadata/entrepo"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/metadata/migrations"
	obslog "github.com/Blue-Door-Software-Group/containers4agents/internal/observability/logging"
)

func TestCountTotalExcludesTombstones(t *testing.T) {
	dsn := os.Getenv("C4A_TEST_DB_DSN")
	if dsn == "" {
		t.Skip("C4A_TEST_DB_DSN not set")
	}

	ctx := context.Background()
	repoClient, err := entrepo.New(config.Config{DBDriver: "postgres", DBDSN: dsn, AutoMigrate: true})
	if err != nil {
		t.Fatalf("create ent repo client: %v", err)
	}
	t.Cleanup(func() { _ = repoClient.Close() })
	if err := migrations.Run(ctx, obslog.New("info"), repoClient.Ent, true); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	repo := NewEntContainerRepository(repoClient.Ent)
	before, err := repo.CountTotal(ctx)
	if err != nil {
		t.Fatalf("count total before: %v", err)
	}

	providerRef := "provider-" + uuid.NewString()
	rows := []struct {
		state       string
		providerRef *string
	}{
		{state: "running", providerRef: &providerRef},
		{state: "failed", providerRef: &providerRef},
		{state: "pruned", providerRef: &providerRef},
		{state: "failed", providerRef: nil},
	}
	for _, row := range rows {
		create := repoClient.Ent.ContainerMetadata.Create().
			SetExternalID(uuid.NewString()).
			SetState(row.state).
			SetProfile("prod-strict").
			SetImageVersion("v1").
			SetNillableProviderRef(row.providerRef)
		created, err := create.Save(ctx)
		if err != nil {
			t.Fatalf("create %s row: %v", row.state, err)
		}
		t.Cleanup(func() {
			_ = repoClient.Ent.ContainerMetadata.DeleteOneID(created.ID).Exec(context.Background())
		})
	}

	after, err := repo.CountTotal(ctx)
	if err != nil {
		t.Fatalf("count total after: %v", err)
	}
	// Only the running row and the failed row holding a provider reference count.
	if got := after - before; got != 2 {
		t.Fatalf("expected CountTotal to grow by 2, grew by %d", got)
	}
}
