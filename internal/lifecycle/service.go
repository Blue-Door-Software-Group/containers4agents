package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/Blue-Door-Software-Group/containers4agents/internal/config"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/metadata/repository"
	obslog "github.com/Blue-Door-Software-Group/containers4agents/internal/observability/logging"
	obsmetrics "github.com/Blue-Door-Software-Group/containers4agents/internal/observability/metrics"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/provider/types"
)

var (
	ErrContainerNotFound = errors.New("container not found")
	ErrStateConflict     = errors.New("container state conflict")
	ErrProviderFailure   = errors.New("provider failure")
	ErrCapacityExceeded  = errors.New("capacity exceeded")
)

type Service struct {
	repo     repository.ContainerRepository
	provider types.ContainerProvider
	cfg      config.Config
	metrics  *obsmetrics.Metrics
}

type ContainerView struct {
	ExternalID               string
	State                    string
	Profile                  string
	ImageVersion             string
	CreatedAt                time.Time
	UpdatedAt                time.Time
	LastActivityAt           *time.Time
	IdleStopDeadlineAt       *time.Time
	RetentionPruneDeadlineAt *time.Time
	StoppedAt                *time.Time
	PruneRetryCount          int
	NextPruneRetryAt         *time.Time
	LastPruneError           *string
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

func (s *Service) CreateContainer(ctx context.Context) (*ContainerView, error) {
	started := time.Now()
	outcome := "internal_error"
	defer func() {
		s.observeLifecycle("create", outcome, time.Since(started))
	}()

	if err := s.ensureCapacityForCreate(ctx); err != nil {
		if errors.Is(err, ErrCapacityExceeded) {
			outcome = "capacity_rejected"
		}
		return nil, err
	}

	runningCount, err := s.repo.CountByState(ctx, StateRunning)
	if err != nil {
		outcome = "internal_error"
		return nil, fmt.Errorf("count running containers: %w", err)
	}
	if s.cfg.RunningContainerLimit > 0 && runningCount >= s.cfg.RunningContainerLimit {
		outcome = "capacity_rejected"
		s.recordCapacityEvent("admission", "running_limit_reached")
		return nil, fmt.Errorf("%w: running container limit reached", ErrCapacityExceeded)
	}
	totalCount, err := s.repo.CountTotal(ctx)
	if err != nil {
		outcome = "internal_error"
		return nil, fmt.Errorf("count total containers: %w", err)
	}
	if s.cfg.TotalContainerLimit > 0 && totalCount >= s.cfg.TotalContainerLimit {
		outcome = "capacity_rejected"
		s.recordCapacityEvent("admission", "total_limit_reached")
		return nil, fmt.Errorf("%w: total container limit reached", ErrCapacityExceeded)
	}

	externalID := uuid.NewString()
	row, err := s.repo.Create(ctx, repository.CreateParams{
		ExternalID:   externalID,
		Profile:      s.cfg.DefaultProfile,
		ImageVersion: s.cfg.ActiveImageVersion,
	})
	if err != nil {
		outcome = "internal_error"
		return nil, fmt.Errorf("create metadata row: %w", err)
	}

	providerContainer, err := s.provider.CreateSandbox(ctx, types.SandboxSpec{
		ExternalID:   externalID,
		Profile:      s.cfg.DefaultProfile,
		ImageVersion: s.cfg.ActiveImageVersion,
		Image:        s.cfg.DockerImage,
		Runtime: types.RuntimeOptions{
			CPUMaxCores:         s.cfg.ContainerCPUMaxCores,
			MemoryMaxBytes:      s.cfg.ContainerMemoryMaxBytes,
			TmpDiskMaxBytes:     s.cfg.ContainerTmpDiskMaxBytes,
			PIDsMax:             s.cfg.ContainerPIDsMax,
			ExecUser:            s.cfg.ContainerExecUser,
			DropAllCapabilities: s.cfg.ContainerDropAllCapabilities,
			NoNewPrivileges:     s.cfg.ContainerNoNewPrivileges,
			ReadOnlyRootFS:      s.cfg.ContainerReadOnlyRootFS,
			SeccompProfilePath:  s.cfg.ContainerSeccompProfilePath,
		},
	})
	if err != nil {
		outcome = "provider_failure"
		category := providerFailureCategory(err)
		s.recordProviderFailure("create_sandbox", category)
		obslog.WithRequestID(ctx, slog.Default()).WarnContext(
			ctx,
			"provider create sandbox failed",
			"component", "lifecycle",
			"operation", "create",
			"error_category", obslog.ErrorCategory(err),
			"provider_category", category,
			"error_summary", obslog.ErrorSummary(err),
		)
		if failedState, stateErr := NextState(row.State, TransitionCreateFailed); stateErr == nil {
			_, _ = s.repo.UpdateStateIfCurrent(ctx, repository.UpdateStateParams{
				ExternalID: row.ExternalID,
				State:      failedState,
			}, row.State)
		}
		return nil, fmt.Errorf("%w: create sandbox: %w", ErrProviderFailure, err)
	}

	next, err := NextState(row.State, TransitionCreateSucceeded)
	if err != nil {
		outcome = "state_conflict"
		return nil, err
	}
	now := time.Now().UTC()
	idleDeadline := now.Add(s.cfg.IdleStopTimeout)
	updated, err := s.repo.UpdateStateIfCurrent(ctx, repository.UpdateStateParams{
		ExternalID:                  externalID,
		State:                       next,
		ProviderRef:                 &providerContainer.ProviderRef,
		SetActivityNow:              true,
		IdleStopDeadlineAt:          &idleDeadline,
		ClearRetentionPruneDeadline: true,
		ClearStoppedAt:              true,
		PruneRetryCount:             intPtr(0),
		ClearNextPruneRetryAt:       true,
		ClearLastPruneError:         true,
	}, row.State)
	if err != nil {
		if errors.Is(err, repository.ErrStateConflict) {
			outcome = "state_conflict"
			return nil, fmt.Errorf("%w: create transition race", ErrStateConflict)
		}
		outcome = "internal_error"
		return nil, fmt.Errorf("persist running state: %w", err)
	}
	outcome = "success"
	s.refreshContainerCountMetrics(ctx)
	return mapContainer(updated), nil
}

func (s *Service) GetContainer(ctx context.Context, externalID string) (*ContainerView, error) {
	row, err := s.repo.GetByExternalID(ctx, externalID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrContainerNotFound
		}
		return nil, fmt.Errorf("load metadata row: %w", err)
	}

	return mapContainer(row), nil
}

func (s *Service) StopContainer(ctx context.Context, externalID string) (*ContainerView, error) {
	started := time.Now()
	outcome := "internal_error"
	defer func() {
		s.observeLifecycle("stop", outcome, time.Since(started))
	}()

	row, err := s.repo.GetByExternalID(ctx, externalID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			outcome = "not_found"
			return nil, ErrContainerNotFound
		}
		return nil, fmt.Errorf("load metadata row: %w", err)
	}

	stoppingState, err := NextState(row.State, TransitionStopRequested)
	if err != nil {
		outcome = "state_conflict"
		return nil, err
	}

	if _, err := s.repo.UpdateStateIfCurrent(ctx, repository.UpdateStateParams{
		ExternalID: row.ExternalID,
		State:      stoppingState,
	}, row.State); err != nil {
		if errors.Is(err, repository.ErrStateConflict) {
			outcome = "state_conflict"
			return nil, fmt.Errorf("%w: stop transition race", ErrStateConflict)
		}
		return nil, fmt.Errorf("persist stopping state: %w", err)
	}

	if row.ProviderRef == nil || *row.ProviderRef == "" {
		outcome = "state_conflict"
		return nil, fmt.Errorf("%w: missing provider reference", ErrStateConflict)
	}
	if err := s.provider.StopSandbox(ctx, *row.ProviderRef); err != nil {
		outcome = "provider_failure"
		category := providerFailureCategory(err)
		s.recordProviderFailure("stop_sandbox", category)
		obslog.WithRequestID(ctx, slog.Default()).WarnContext(
			ctx,
			"provider stop sandbox failed",
			"component", "lifecycle",
			"operation", "stop",
			"error_category", obslog.ErrorCategory(err),
			"provider_category", category,
			"error_summary", obslog.ErrorSummary(err),
		)
		_, _ = s.repo.UpdateStateIfCurrent(ctx, repository.UpdateStateParams{
			ExternalID: row.ExternalID,
			State:      StateFailed,
		}, stoppingState)
		return nil, fmt.Errorf("%w: stop sandbox: %w", ErrProviderFailure, err)
	}

	stoppedState, err := NextState(stoppingState, TransitionStopCompleted)
	if err != nil {
		outcome = "state_conflict"
		return nil, err
	}
	now := time.Now().UTC()
	retentionDeadline := now.Add(s.cfg.StoppedRetentionTimeout)
	updated, err := s.repo.UpdateStateIfCurrent(ctx, repository.UpdateStateParams{
		ExternalID:               row.ExternalID,
		State:                    stoppedState,
		SetActivityNow:           true,
		StoppedAt:                &now,
		RetentionPruneDeadlineAt: &retentionDeadline,
		ClearIdleStopDeadline:    true,
	}, stoppingState)
	if err != nil {
		if errors.Is(err, repository.ErrStateConflict) {
			outcome = "state_conflict"
			return nil, fmt.Errorf("%w: stop completion transition race", ErrStateConflict)
		}
		return nil, fmt.Errorf("persist stopped state: %w", err)
	}
	outcome = "success"
	s.refreshContainerCountMetrics(ctx)
	return mapContainer(updated), nil
}

func (s *Service) StartContainer(ctx context.Context, externalID string) (*ContainerView, error) {
	started := time.Now()
	outcome := "internal_error"
	defer func() {
		s.observeLifecycle("start", outcome, time.Since(started))
	}()

	if err := s.ensureCapacityForStart(ctx); err != nil {
		if errors.Is(err, ErrCapacityExceeded) {
			outcome = "capacity_rejected"
		}
		return nil, err
	}

	row, err := s.repo.GetByExternalID(ctx, externalID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			outcome = "not_found"
			return nil, ErrContainerNotFound
		}
		return nil, fmt.Errorf("load metadata row: %w", err)
	}
	if row.ProviderRef == nil || *row.ProviderRef == "" {
		outcome = "state_conflict"
		return nil, fmt.Errorf("%w: missing provider reference", ErrStateConflict)
	}

	startingState, err := NextState(row.State, TransitionStartRequested)
	if err != nil {
		outcome = "state_conflict"
		return nil, err
	}
	if _, err := s.repo.UpdateStateIfCurrent(ctx, repository.UpdateStateParams{
		ExternalID: row.ExternalID,
		State:      startingState,
	}, row.State); err != nil {
		if errors.Is(err, repository.ErrStateConflict) {
			outcome = "state_conflict"
			return nil, fmt.Errorf("%w: start transition race", ErrStateConflict)
		}
		return nil, fmt.Errorf("persist starting state: %w", err)
	}

	if err := s.provider.StartSandbox(ctx, *row.ProviderRef); err != nil {
		outcome = "provider_failure"
		category := providerFailureCategory(err)
		s.recordProviderFailure("start_sandbox", category)
		obslog.WithRequestID(ctx, slog.Default()).WarnContext(
			ctx,
			"provider start sandbox failed",
			"component", "lifecycle",
			"operation", "start",
			"error_category", obslog.ErrorCategory(err),
			"provider_category", category,
			"error_summary", obslog.ErrorSummary(err),
		)
		if failedState, stateErr := NextState(startingState, TransitionStartFailed); stateErr == nil {
			_, _ = s.repo.UpdateStateIfCurrent(ctx, repository.UpdateStateParams{
				ExternalID: row.ExternalID,
				State:      failedState,
			}, startingState)
		}
		return nil, fmt.Errorf("%w: start sandbox: %w", ErrProviderFailure, err)
	}

	runningState, err := NextState(startingState, TransitionStartCompleted)
	if err != nil {
		outcome = "state_conflict"
		return nil, err
	}
	now := time.Now().UTC()
	idleDeadline := now.Add(s.cfg.IdleStopTimeout)
	updated, err := s.repo.UpdateStateIfCurrent(ctx, repository.UpdateStateParams{
		ExternalID:                  row.ExternalID,
		State:                       runningState,
		SetActivityNow:              true,
		IdleStopDeadlineAt:          &idleDeadline,
		ClearRetentionPruneDeadline: true,
		ClearStoppedAt:              true,
		PruneRetryCount:             intPtr(0),
		ClearNextPruneRetryAt:       true,
		ClearLastPruneError:         true,
	}, startingState)
	if err != nil {
		if errors.Is(err, repository.ErrStateConflict) {
			outcome = "state_conflict"
			return nil, fmt.Errorf("%w: start completion transition race", ErrStateConflict)
		}
		return nil, fmt.Errorf("persist running state from start: %w", err)
	}
	outcome = "success"
	s.refreshContainerCountMetrics(ctx)
	return mapContainer(updated), nil
}

func (s *Service) PruneContainer(ctx context.Context, externalID string) (*ContainerView, error) {
	started := time.Now()
	outcome := "internal_error"
	defer func() {
		s.observeLifecycle("prune", outcome, time.Since(started))
	}()

	row, err := s.repo.GetByExternalID(ctx, externalID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			outcome = "not_found"
			return nil, ErrContainerNotFound
		}
		return nil, fmt.Errorf("load metadata row: %w", err)
	}
	if row.ProviderRef == nil || *row.ProviderRef == "" {
		outcome = "state_conflict"
		return nil, fmt.Errorf("%w: missing provider reference", ErrStateConflict)
	}

	pruningState, err := NextState(row.State, TransitionPruneRequested)
	if err != nil {
		outcome = "state_conflict"
		return nil, err
	}
	if _, err := s.repo.UpdateStateIfCurrent(ctx, repository.UpdateStateParams{
		ExternalID: row.ExternalID,
		State:      pruningState,
	}, row.State); err != nil {
		if errors.Is(err, repository.ErrStateConflict) {
			outcome = "state_conflict"
			return nil, fmt.Errorf("%w: prune transition race", ErrStateConflict)
		}
		return nil, fmt.Errorf("persist pruning state: %w", err)
	}

	if err := s.provider.PruneSandbox(ctx, *row.ProviderRef); err != nil {
		outcome = "provider_failure"
		category := providerFailureCategory(err)
		s.recordProviderFailure("prune_sandbox", category)
		obslog.WithRequestID(ctx, slog.Default()).WarnContext(
			ctx,
			"provider prune sandbox failed",
			"component", "lifecycle",
			"operation", "prune",
			"error_category", obslog.ErrorCategory(err),
			"provider_category", category,
			"error_summary", obslog.ErrorSummary(err),
		)
		if failedState, stateErr := NextState(pruningState, TransitionPruneFailed); stateErr == nil {
			nextRetryAt := time.Now().UTC().Add(backoffForRetry(row.PruneRetryCount))
			retryCount := row.PruneRetryCount + 1
			lastErr := err.Error()
			_, _ = s.repo.UpdateStateIfCurrent(ctx, repository.UpdateStateParams{
				ExternalID:       row.ExternalID,
				State:            failedState,
				PruneRetryCount:  &retryCount,
				NextPruneRetryAt: &nextRetryAt,
				LastPruneError:   &lastErr,
			}, pruningState)
		}
		return nil, fmt.Errorf("%w: prune sandbox: %w", ErrProviderFailure, err)
	}

	prunedState, err := NextState(pruningState, TransitionPruneCompleted)
	if err != nil {
		outcome = "state_conflict"
		return nil, err
	}
	updated, err := s.repo.UpdateStateIfCurrent(ctx, repository.UpdateStateParams{
		ExternalID:                  row.ExternalID,
		State:                       prunedState,
		SetActivityNow:              true,
		ClearIdleStopDeadline:       true,
		ClearRetentionPruneDeadline: true,
		ClearStoppedAt:              true,
		PruneRetryCount:             intPtr(0),
		ClearNextPruneRetryAt:       true,
		ClearLastPruneError:         true,
	}, pruningState)
	if err != nil {
		if errors.Is(err, repository.ErrStateConflict) {
			outcome = "state_conflict"
			return nil, fmt.Errorf("%w: prune completion transition race", ErrStateConflict)
		}
		return nil, fmt.Errorf("persist pruned state: %w", err)
	}
	outcome = "success"
	s.refreshContainerCountMetrics(ctx)
	return mapContainer(updated), nil
}

func mapContainer(c *repository.Container) *ContainerView {
	return &ContainerView{
		ExternalID:               c.ExternalID,
		State:                    c.State,
		Profile:                  c.Profile,
		ImageVersion:             c.ImageVersion,
		CreatedAt:                c.CreatedAt,
		UpdatedAt:                c.UpdatedAt,
		LastActivityAt:           c.LastActivityAt,
		IdleStopDeadlineAt:       c.IdleStopDeadlineAt,
		RetentionPruneDeadlineAt: c.RetentionPruneDeadlineAt,
		StoppedAt:                c.StoppedAt,
		PruneRetryCount:          c.PruneRetryCount,
		NextPruneRetryAt:         c.NextPruneRetryAt,
		LastPruneError:           c.LastPruneError,
	}
}

func backoffForRetry(currentRetryCount int) time.Duration {
	backoff := time.Duration(currentRetryCount+1) * time.Minute
	if backoff > 30*time.Minute {
		return 30 * time.Minute
	}
	return backoff
}

func intPtr(v int) *int {
	return &v
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

func (s *Service) refreshContainerCountMetrics(ctx context.Context) {
	if s.metrics == nil {
		return
	}
	running, err := s.repo.CountByState(ctx, StateRunning)
	if err != nil {
		return
	}
	stopped, err := s.repo.CountByState(ctx, StateStopped)
	if err != nil {
		return
	}
	total, err := s.repo.CountTotal(ctx)
	if err != nil {
		return
	}
	s.metrics.SetContainerCounts(running, stopped, total)
}

func (s *Service) ensureCapacityForCreate(ctx context.Context) error {
	if s.cfg.RunningContainerLimit <= 0 && s.cfg.TotalContainerLimit <= 0 {
		return nil
	}

	runningCount, err := s.repo.CountByState(ctx, StateRunning)
	if err != nil {
		return fmt.Errorf("count running containers: %w", err)
	}
	totalCount, err := s.repo.CountTotal(ctx)
	if err != nil {
		return fmt.Errorf("count total containers: %w", err)
	}

	if s.cfg.TotalContainerLimit > 0 && totalCount >= s.cfg.TotalContainerLimit {
		s.recordCapacityEvent("admission", "total_limit_reached")
		if reclaimed := s.reclaimOneForTotal(ctx); !reclaimed {
			s.recordCapacityEvent("reclaim_total", "failed")
			return fmt.Errorf("%w: total container limit reached", ErrCapacityExceeded)
		}
		s.recordCapacityEvent("reclaim_total", "succeeded")
	}
	if s.cfg.RunningContainerLimit > 0 && runningCount >= s.cfg.RunningContainerLimit {
		s.recordCapacityEvent("admission", "running_limit_reached")
		if reclaimed := s.reclaimOneForRunning(ctx); !reclaimed {
			s.recordCapacityEvent("reclaim_running", "failed")
			return fmt.Errorf("%w: running container limit reached", ErrCapacityExceeded)
		}
		s.recordCapacityEvent("reclaim_running", "succeeded")
	}
	return nil
}

func (s *Service) ensureCapacityForStart(ctx context.Context) error {
	if s.cfg.RunningContainerLimit <= 0 {
		return nil
	}
	runningCount, err := s.repo.CountByState(ctx, StateRunning)
	if err != nil {
		return fmt.Errorf("count running containers: %w", err)
	}
	if runningCount < s.cfg.RunningContainerLimit {
		return nil
	}
	s.recordCapacityEvent("admission", "running_limit_reached")
	if reclaimed := s.reclaimOneForRunning(ctx); !reclaimed {
		s.recordCapacityEvent("reclaim_running", "failed")
		return fmt.Errorf("%w: running container limit reached", ErrCapacityExceeded)
	}
	s.recordCapacityEvent("reclaim_running", "succeeded")
	return nil
}

func (s *Service) reclaimOneForTotal(ctx context.Context) bool {
	if rows, err := s.repo.FindExpiredStoppedRetention(ctx, time.Now().UTC(), 1); err == nil && len(rows) > 0 {
		_, pruneErr := s.PruneContainer(ctx, rows[0].ExternalID)
		return pruneErr == nil
	}
	if rows, err := s.repo.FindLongestIdleStopped(ctx, 1); err == nil && len(rows) > 0 {
		_, pruneErr := s.PruneContainer(ctx, rows[0].ExternalID)
		return pruneErr == nil
	}
	if rows, err := s.repo.FindExpiredRunningIdle(ctx, time.Now().UTC(), 1); err == nil && len(rows) > 0 {
		_, pruneErr := s.PruneContainer(ctx, rows[0].ExternalID)
		return pruneErr == nil
	}
	if rows, err := s.repo.FindLongestIdleRunningNoExec(ctx, 1); err == nil && len(rows) > 0 {
		_, pruneErr := s.PruneContainer(ctx, rows[0].ExternalID)
		return pruneErr == nil
	}
	return false
}

func (s *Service) reclaimOneForRunning(ctx context.Context) bool {
	if rows, err := s.repo.FindExpiredRunningIdle(ctx, time.Now().UTC(), 1); err == nil && len(rows) > 0 {
		_, stopErr := s.StopContainer(ctx, rows[0].ExternalID)
		return stopErr == nil
	}
	if rows, err := s.repo.FindLongestIdleRunningNoExec(ctx, 1); err == nil && len(rows) > 0 {
		_, stopErr := s.StopContainer(ctx, rows[0].ExternalID)
		return stopErr == nil
	}
	return false
}

func (s *Service) observeLifecycle(operation string, outcome string, d time.Duration) {
	if s.metrics == nil {
		return
	}
	s.metrics.ObserveLifecycle(operation, outcome, d)
}

func (s *Service) recordCapacityEvent(event string, outcome string) {
	if s.metrics == nil {
		return
	}
	s.metrics.RecordCapacityEvent(event, outcome)
}

func (s *Service) recordProviderFailure(operation string, category string) {
	if s.metrics == nil {
		return
	}
	s.metrics.RecordProviderFailure(operation, category)
}
