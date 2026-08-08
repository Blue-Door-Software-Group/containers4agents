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
	obslog "github.com/Blue-Door-Software-Group/containers4agents/internal/observability/logging"
	obsmetrics "github.com/Blue-Door-Software-Group/containers4agents/internal/observability/metrics"
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
	metrics  *obsmetrics.Metrics
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

func NewService(repo repository.ContainerRepository, provider types.ContainerProvider, cfg config.Config, metrics ...*obsmetrics.Metrics) *Service {
	var m *obsmetrics.Metrics
	if len(metrics) > 0 {
		m = metrics[0]
	}
	return &Service{
		repo:     repo,
		provider: provider,
		cfg:      cfg,
		metrics:  m,
	}
}

func (s *Service) Execute(ctx context.Context, externalID string, command []string) (*Result, error) {
	execStarted := time.Now()
	outcome := StatusInternalError
	defer func() {
		s.observeExecution(outcome, time.Since(execStarted))
	}()

	if externalID == "" {
		outcome = "invalid_request"
		return nil, fmt.Errorf("%w: missing container id", ErrInvalidCommand)
	}
	if len(command) == 0 {
		outcome = "invalid_request"
		return nil, fmt.Errorf("%w: command must not be empty", ErrInvalidCommand)
	}

	row, err := s.repo.GetByExternalID(ctx, externalID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			outcome = "not_found"
			return nil, ErrContainerNotFound
		}
		return nil, fmt.Errorf("load metadata row: %w", err)
	}
	if row.State != lifecycle.StateRunning {
		outcome = "state_conflict"
		return nil, fmt.Errorf("%w: container must be running (state=%s)", ErrStateConflict, row.State)
	}
	if row.ProviderRef == nil || *row.ProviderRef == "" {
		outcome = "state_conflict"
		return nil, fmt.Errorf("%w: missing provider reference", ErrStateConflict)
	}

	activeExecCount, err := s.repo.CountExecutionInProgress(ctx)
	if err != nil {
		return nil, fmt.Errorf("count active executions: %w", err)
	}
	s.setExecInProgress(activeExecCount)
	if s.cfg.ActiveExecLimit > 0 && activeExecCount >= s.cfg.ActiveExecLimit {
		outcome = "state_conflict"
		return nil, fmt.Errorf("%w: active execution limit reached", ErrStateConflict)
	}
	if err := s.repo.BeginExecution(ctx, row.ExternalID, lifecycle.StateRunning); err != nil {
		switch {
		case errors.Is(err, repository.ErrExecutionInProgress):
			outcome = "state_conflict"
			return nil, fmt.Errorf("%w: execution already in progress", ErrStateConflict)
		case errors.Is(err, repository.ErrStateConflict):
			outcome = "state_conflict"
			return nil, fmt.Errorf("%w: container must be running (state changed)", ErrStateConflict)
		case errors.Is(err, repository.ErrNotFound):
			outcome = "not_found"
			return nil, ErrContainerNotFound
		default:
			return nil, fmt.Errorf("begin execution lease: %w", err)
		}
	}
	defer func() {
		if endErr := s.repo.EndExecution(ctx, row.ExternalID); endErr != nil && !errors.Is(endErr, repository.ErrNotFound) {
			obslog.WithRequestID(ctx, slog.Default()).WarnContext(ctx, "failed to release execution lease", "component", "execution", "operation", "exec", "error_category", obslog.ErrorCategory(endErr))
		}
		if count, countErr := s.repo.CountExecutionInProgress(ctx); countErr == nil {
			s.setExecInProgress(count)
		}
	}()

	providerStarted := time.Now()
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
		outcome = "provider_failure"
		s.recordProviderFailure("exec", providerFailureCategory(err))
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
		Duration:       time.Since(providerStarted),
		AppliedTimeout: s.cfg.ExecTimeout,
	}

	_, updateErr := s.repo.UpdateStateIfCurrent(ctx, repository.UpdateStateParams{
		ExternalID:         row.ExternalID,
		State:              lifecycle.StateRunning,
		SetActivityNow:     true,
		IdleStopDeadlineAt: timePtr(time.Now().UTC().Add(s.cfg.IdleStopTimeout)),
	}, lifecycle.StateRunning)
	if updateErr != nil && !errors.Is(updateErr, repository.ErrStateConflict) && !errors.Is(updateErr, repository.ErrNotFound) {
		obslog.WithRequestID(ctx, slog.Default()).WarnContext(ctx, "failed to persist execution activity timestamp", "component", "execution", "operation", "exec", "error_category", obslog.ErrorCategory(updateErr))
	}
	outcome = result.Status
	return result, nil
}

func timePtr(t time.Time) *time.Time {
	return &t
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

func providerFailureCategory(err error) string {
	switch {
	case errors.Is(err, types.ErrProviderUnavailable):
		return "provider_unavailable"
	case errors.Is(err, types.ErrProviderTimeout):
		return "provider_timeout"
	case errors.Is(err, types.ErrProviderConflict):
		return "provider_conflict"
	case errors.Is(err, types.ErrProviderNotFound):
		return "provider_not_found"
	case errors.Is(err, types.ErrProviderDenied):
		return "provider_denied"
	case errors.Is(err, types.ErrProviderQuotaExceeded):
		return "provider_quota"
	default:
		return "provider_internal"
	}
}

func (s *Service) observeExecution(outcome string, d time.Duration) {
	if s.metrics == nil {
		return
	}
	s.metrics.ObserveExecution(outcome, d)
}

func (s *Service) setExecInProgress(count int) {
	if s.metrics == nil {
		return
	}
	s.metrics.SetExecInProgress(count)
}

func (s *Service) recordProviderFailure(operation string, category string) {
	if s.metrics == nil {
		return
	}
	s.metrics.RecordProviderFailure(operation, category)
}
