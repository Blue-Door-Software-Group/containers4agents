package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Blue-Door-Software-Group/containers4agents/internal/config"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/metadata/entrepo"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/metadata/migrations"
	obslog "github.com/Blue-Door-Software-Group/containers4agents/internal/observability/logging"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/observability/metrics"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/server"
)

func main() {
	if err := run(); err != nil {
		slog.Error("service exited with error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger := obslog.New(cfg.LogLevel)
	logger.Info("starting containers4agents", "addr", cfg.HTTPListenAddr, "db_driver", cfg.DBDriver, "auto_migrate", cfg.AutoMigrate)

	repoClient, err := entrepo.New(cfg)
	if err != nil {
		return fmt.Errorf("initialize ent repository client: %w", err)
	}
	defer func() {
		if closeErr := repoClient.Close(); closeErr != nil {
			logger.Error("failed to close ent client", "error", closeErr)
		}
	}()

	startupCtx, startupCancel := context.WithTimeout(context.Background(), cfg.StartupTimeout)
	defer startupCancel()

	if err := repoClient.DB.PingContext(startupCtx); err != nil {
		return fmt.Errorf("ping postgres: %w", err)
	}

	if err := migrations.Run(startupCtx, logger, repoClient.Ent, cfg.AutoMigrate); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}

	metricsRegistry := metrics.New()
	handler := server.NewHandler(metricsRegistry)
	apiServer := server.New(cfg, logger, handler)

	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(signalCh)

	serverErrCh := make(chan error, 1)
	go func() {
		serverErrCh <- apiServer.Start()
	}()

	shutdownWithTimeout := func(sig os.Signal) error {
		logger.Info("shutdown signal received", "signal", sig.String())
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer shutdownCancel()

		shutdownErr := apiServer.Shutdown(shutdownCtx)
		if shutdownErr != nil {
			return fmt.Errorf("shutdown server: %w", shutdownErr)
		}

		select {
		case startErr := <-serverErrCh:
			return startErr
		case <-shutdownCtx.Done():
			return fmt.Errorf("server did not exit before shutdown timeout: %w", shutdownCtx.Err())
		}
	}

	for {
		select {
		case startErr := <-serverErrCh:
			return startErr
		case sig := <-signalCh:
			return shutdownWithTimeout(sig)
		}
	}
}
