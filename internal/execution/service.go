package execution

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Blue-Door-Software-Group/containers4agents/internal/config"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/lifecycle"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/metadata/repository"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/provider/types"
)

const (
	StatusOK               = "ok"
	StatusUserError        = "user_error"
	StatusTimeout          = "timeout"
	StatusOOM              = "oom"
	StatusDiskQuota        = "disk_quota"
	StatusSandboxViolation = "sandbox_violation"
	StatusKilled           = "killed"
	StatusInternalError    = "internal_error"
)

var (
	ErrContainerNotFound = errors.New("container not found")
	ErrStateConflict     = errors.New("execution state conflict")
	ErrProviderFailure   = errors.New("execution provider failure")
	ErrInvalidCommand    = errors.New("invalid command")
)

type Service struct {
	repo     repository.ContainerRepository
	provider types.ContainerProvider
	cfg      config.Config
}

type Result struct {
	ContainerID    string
	Status         string
	ExitCode       *int
	Stdout         string
	Stderr         string
	TruncatedOut   bool
	TruncatedErr   bool
	Duration       time.Duration
	AppliedTimeout time.Duration
}

func NewService(repo repository.ContainerRepository, provider types.ContainerProvider, cfg config.Config) *Service {
	return &Service{
		repo:     repo,
		provider: provider,
		cfg:      cfg,
	}
}

func (s *Service) Execute(ctx context.Context, externalID string, command []string) (*Result, error) {
	if externalID == "" {
		return nil, fmt.Errorf("%w: missing container id", ErrInvalidCommand)
	}
	if len(command) == 0 {
		return nil, fmt.Errorf("%w: command must not be empty", ErrInvalidCommand)
	}

	row, err := s.repo.GetByExternalID(ctx, externalID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrContainerNotFound
		}
		return nil, fmt.Errorf("load metadata row: %w", err)
	}
	if row.State != lifecycle.StateRunning {
		return nil, fmt.Errorf("%w: container must be running (state=%s)", ErrStateConflict, row.State)
	}
	if row.ProviderRef == nil || *row.ProviderRef == "" {
		return nil, fmt.Errorf("%w: missing provider reference", ErrStateConflict)
	}

	activeExecCount, err := s.repo.CountExecutionInProgress(ctx)
	if err != nil {
		return nil, fmt.Errorf("count active executions: %w", err)
	}
	if s.cfg.ActiveExecLimit > 0 && activeExecCount >= s.cfg.ActiveExecLimit {
		return nil, fmt.Errorf("%w: active execution limit reached", ErrStateConflict)
	}
	if err := s.repo.BeginExecution(ctx, row.ExternalID, lifecycle.StateRunning); err != nil {
		switch {
		case errors.Is(err, repository.ErrExecutionInProgress):
			return nil, fmt.Errorf("%w: execution already in progress", ErrStateConflict)
		case errors.Is(err, repository.ErrStateConflict):
			return nil, fmt.Errorf("%w: container must be running (state changed)", ErrStateConflict)
		case errors.Is(err, repository.ErrNotFound):
			return nil, ErrContainerNotFound
		default:
			return nil, fmt.Errorf("begin execution lease: %w", err)
		}
	}
	defer func() {
		if endErr := s.repo.EndExecution(ctx, row.ExternalID); endErr != nil && !errors.Is(endErr, repository.ErrNotFound) {
			slog.WarnContext(ctx, "failed to release execution lease", "external_id", row.ExternalID, "error", endErr)
		}
	}()

	started := time.Now()
	execCtx, cancel := context.WithTimeout(ctx, s.cfg.ExecTimeout)
	defer cancel()

	providerResult, err := s.provider.Exec(execCtx, *row.ProviderRef, types.ExecRequest{
		Command:        command,
		Timeout:        s.cfg.ExecTimeout,
		MaxStdoutBytes: s.cfg.ExecMaxStdoutBytes,
		MaxStderrBytes: s.cfg.ExecMaxStderrBytes,
		User:           s.cfg.ContainerExecUser,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: execute command: %w", ErrProviderFailure, err)
	}

	result := &Result{
		ContainerID:    row.ExternalID,
		Status:         normalizeStatus(providerResult.Status, providerResult.ExitCode),
		ExitCode:       providerResult.ExitCode,
		Stdout:         providerResult.Stdout,
		Stderr:         providerResult.Stderr,
		TruncatedOut:   providerResult.TruncatedOut,
		TruncatedErr:   providerResult.TruncatedErr,
		Duration:       time.Since(started),
		AppliedTimeout: s.cfg.ExecTimeout,
	}

	_, updateErr := s.repo.UpdateStateIfCurrent(ctx, repository.UpdateStateParams{
		ExternalID:     row.ExternalID,
		State:          lifecycle.StateRunning,
		SetActivityNow: true,
	}, lifecycle.StateRunning)
	if updateErr != nil && !errors.Is(updateErr, repository.ErrStateConflict) && !errors.Is(updateErr, repository.ErrNotFound) {
		slog.WarnContext(ctx, "failed to persist execution activity timestamp", "external_id", row.ExternalID, "error", updateErr)
	}

	return result, nil
}

func normalizeStatus(status string, exitCode *int) string {
	switch status {
	case StatusOK, StatusUserError, StatusTimeout, StatusOOM, StatusDiskQuota, StatusSandboxViolation, StatusKilled, StatusInternalError:
		return status
	}

	if exitCode == nil {
		return StatusInternalError
	}
	if *exitCode == 0 {
		return StatusOK
	}
	return StatusUserError
}
