package main

import (
	"io"
	"log/slog"
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

func TestApplyResourcePolicy_Images(t *testing.T) {
	t.Setenv("C4A_TEST_DEV_IMAGE", "ghcr.io/x/c4a-swarm-dev:v1")
	p := policy.Defaults()
	dev := p.Profiles[policy.DefaultProfileName]
	dev.Memory.MaxBytes = 8 << 30
	dev.Files.MaxUploadBytes = 256 << 20
	p.Profiles["dev-egress"] = dev
	p.Images = map[string]policy.ImageSpec{
		"swarm-dev": {Ref: "${C4A_TEST_DEV_IMAGE}", Profile: "dev-egress", Network: "egress", IdleStopTimeout: "6h", StoppedRetentionTimeout: "720h"},
	}
	cfg := config.Config{DockerImage: "alpine:3.20", IdleStopTimeout: 20 * time.Minute, StoppedRetentionTimeout: 24 * time.Hour}
	if err := applyResourcePolicy(&cfg, p); err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultImageName != policy.ImplicitImageName {
		t.Fatalf("default image %q", cfg.DefaultImageName)
	}
	rt, ok := cfg.Image("")
	if !ok || rt.Settings.DockerImage != "alpine:3.20" || rt.Network != policy.NetworkNone || rt.Settings.IdleStopTimeout != 20*time.Minute {
		t.Fatalf("implicit image %+v", rt)
	}
	sd, ok := cfg.Image("swarm-dev")
	if !ok || sd.Settings.DockerImage != "ghcr.io/x/c4a-swarm-dev:v1" || sd.Network != policy.NetworkEgress ||
		sd.Settings.IdleStopTimeout != 6*time.Hour || sd.Settings.StoppedRetentionTimeout != 720*time.Hour ||
		sd.Settings.ContainerMemoryMaxBytes != 8<<30 || sd.Settings.DefaultProfile != "dev-egress" {
		t.Fatalf("swarm-dev %+v", sd)
	}
	if maxUploadBytes(cfg) != 256<<20 {
		t.Fatalf("max upload %d", maxUploadBytes(cfg))
	}
	if cfg.ForImage("").DockerImage != "alpine:3.20" || cfg.ForImage("gone").DockerImage != "alpine:3.20" {
		t.Fatal("ForImage falls back to the default image")
	}
	infos := imageInfos(cfg)
	if len(infos) != 2 || infos[0].Name != "runtime" || !infos[0].Default || infos[1].Network != "egress" {
		t.Fatalf("infos %+v", infos)
	}

	checked := false
	warnUnguardedEgress(slog.New(slog.NewTextHandler(io.Discard, nil)), cfg, func() (bool, bool) { checked = true; return true, true })
	if !checked {
		t.Fatal("egress images are checked")
	}
	plain := config.Config{}
	warnUnguardedEgress(slog.New(slog.NewTextHandler(io.Discard, nil)), plain, func() (bool, bool) { t.Fatal("no egress, no check"); return false, false })
}
