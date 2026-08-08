package main

import (
	"testing"
	"time"

	"github.com/Blue-Door-Software-Group/containers4agents/internal/config"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/policy"
)

func TestApplyResourcePolicy(t *testing.T) {
	cfg := config.Config{DefaultProfile: "prod-strict"}
	p := policy.Defaults()

	if err := applyResourcePolicy(&cfg, p); err != nil {
		t.Fatalf("apply policy: %v", err)
	}

	if cfg.ActiveExecLimit != 10 || cfg.RunningContainerLimit != 30 || cfg.TotalContainerLimit != 100 {
		t.Fatalf("unexpected capacity limits: %+v", cfg)
	}
	if cfg.ContainerCPUMaxCores != 2.0 {
		t.Fatalf("expected cpu max cores 2.0, got %f", cfg.ContainerCPUMaxCores)
	}
	if cfg.ExecTimeout != 120*time.Second {
		t.Fatalf("expected exec timeout 120s, got %s", cfg.ExecTimeout)
	}
	if cfg.FileMaxUploadBytes != 64*1024*1024 || cfg.FileMaxDownloadBytes != 64*1024*1024 {
		t.Fatalf("unexpected file transfer caps: upload=%d download=%d", cfg.FileMaxUploadBytes, cfg.FileMaxDownloadBytes)
	}
}
