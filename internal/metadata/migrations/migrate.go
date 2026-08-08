package migrations

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Blue-Door-Software-Group/containers4agents/ent"
)

func Run(ctx context.Context, logger *slog.Logger, client *ent.Client, autoMigrate bool) error {
	if !autoMigrate {
		logger.Info("AUTO_MIGRATE disabled; skipping schema migration")
		return nil
	}

	if err := client.Schema.Create(ctx); err != nil {
		return fmt.Errorf("run ent schema migration: %w", err)
	}

	logger.Info("schema migration complete")
	return nil
}
