package migrations

import (
	"context"
	"testing"

	obslog "github.com/Blue-Door-Software-Group/containers4agents/internal/observability/logging"
)

func TestRunSkipsWhenAutoMigrateDisabled(t *testing.T) {
	logger := obslog.New("debug")
	err := Run(context.Background(), logger, nil, false)
	if err != nil {
		t.Fatalf("expected nil error when auto migrate disabled, got: %v", err)
	}
}
