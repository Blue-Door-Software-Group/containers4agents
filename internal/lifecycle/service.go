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
	// ErrUnknownImage: the create named an image the server has not registered.
	ErrUnknownImage = errors.New("unknown image")
	// ErrPolicyNotAllowed: the create asked for a lifecycle or storage policy its image does not
	// allow (or one that does not exist).
	ErrPolicyNotAllowed = errors.New("policy not allowed for this image")
	// ErrContainerExists: the create's external id names a live container (see ExistsError).
	ErrContainerExists   = errors.New("container already exists")
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

// CreateOptions are a create's optional choices; the zero value is the create before images
// existed: the default image and a server-generated id.
type CreateOptions struct {
	// Image is a registered image name; empty means the default image.
	Image string
	// ExternalID is the caller's id for the container; empty means a generated UUID. A pruned or
	// failed container's id may be reused.
	ExternalID string
	// ReturnExisting returns a live container that already has ExternalID instead of failing
	// with an ExistsError.
	ReturnExisting bool
	// Lifecycle is "" (the default: pruned after its retention) or LifecycleStopOnly.
	Lifecycle string
	// Storage is "" (the container's own layer) or StoragePersistent (/workspace on a named volume
	// that outlives the container).
	Storage string
}

// Lifecycle and storage policies a create may ask for (its image must allow them).
const (
	LifecycleStopOnly = repository.LifecycleStopOnly
	StoragePersistent = "persistent"
)

// PruneOptions tune a prune: Purge also removes a persistent container's workspace volume.
type PruneOptions struct{ Purge bool }

// ExistsError is ErrContainerExists with the existing container.
type ExistsError struct{ View *ContainerView }

func (e *ExistsError) Error() string { return ErrContainerExists.Error() }
func (e *ExistsError) Unwrap() error { return ErrContainerExists }

type ContainerView struct {
	ExternalID   string
	State        string
	Profile      string
	ImageVersion string
	// ImageName is the registered image ("" on rows from before images) and Network its mode.
	ImageName string
	Network   string
	// Lifecycle and Storage are its policies ("" for the defaults).
	Lifecycle string
	Storage   string
	// Existed: CreateContainer returned an existing container (ReturnExisting).
	Existed                  bool
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

func (s *Service) CreateContainer(ctx context.Context, opts CreateOptions) (*ContainerView, error) {
	started := time.Now()
	outcome := "internal_error"
	defer func() {
		s.observeLifecycle("create", outcome, time.Since(started))
	}()

	img, ok := s.cfg.Image(opts.Image)
	if !ok {
		outcome = "invalid_request"
		return nil, fmt.Errorf("%w: %q", ErrUnknownImage, opts.Image)
	}
	settings := img.Settings
	switch {
	case opts.Lifecycle != "" && (opts.Lifecycle != LifecycleStopOnly || !img.AllowStopOnly):
		outcome = "invalid_request"
		return nil, fmt.Errorf("%w: lifecycle %q on image %q", ErrPolicyNotAllowed, opts.Lifecycle, img.Name)
	case opts.Storage != "" && (opts.Storage != StoragePersistent || !img.AllowPersistent):
		outcome = "invalid_request"
		return nil, fmt.Errorf("%w: storage %q on image %q", ErrPolicyNotAllowed, opts.Storage, img.Name)
	}
	if opts.ExternalID != "" {
		if existing, err := s.liveContainer(ctx, opts.ExternalID); err != nil || existing != nil {
			if err != nil {
				return nil, err
			}
			outcome = "exists"
			if opts.ReturnExisting {
				existing.Existed = true
				return existing, nil
			}
			return nil, &ExistsError{View: existing}
		}
	}

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

	externalID := opts.ExternalID
	if externalID == "" {
		externalID = uuid.NewString()
	} else if err := s.removeFailedSandbox(ctx, externalID); err != nil {
		outcome = "provider_failure"
		return nil, err
	}
	imageName := ""
	if len(s.cfg.Images) > 0 {
		imageName = img.Name
	}
	row, err := s.repo.Create(ctx, repository.CreateParams{
		ExternalID:      externalID,
		Profile:         settings.DefaultProfile,
		ImageVersion:    s.cfg.ActiveImageVersion,
		ImageName:       imageName,
		LifecyclePolicy: opts.Lifecycle,
		StoragePolicy:   opts.Storage,
		ReplaceTerminal: opts.ExternalID != "",
	})
	if errors.Is(err, repository.ErrExternalIDTaken) {
		// A concurrent create took the id first: it exists now.
		existing, getErr := s.liveContainer(ctx, externalID)
		if getErr != nil || existing == nil {
			outcome = "state_conflict"
			return nil, fmt.Errorf("%w: external id in use", ErrStateConflict)
		}
		outcome = "exists"
		if opts.ReturnExisting {
			existing.Existed = true
			return existing, nil
		}
		return nil, &ExistsError{View: existing}
	}
	if err != nil {
		outcome = "internal_error"
		return nil, fmt.Errorf("create metadata row: %w", err)
	}

	providerContainer, err := s.provider.CreateSandbox(ctx, types.SandboxSpec{
		ExternalID:   externalID,
		Profile:      settings.DefaultProfile,
		ImageVersion: s.cfg.ActiveImageVersion,
		Image:        settings.DockerImage,
		Network:      img.Network,
		Persistent:   opts.Storage == StoragePersistent,
		Runtime: types.RuntimeOptions{
			CPUMaxCores:         settings.ContainerCPUMaxCores,
			MemoryMaxBytes:      settings.ContainerMemoryMaxBytes,
			TmpDiskMaxBytes:     settings.ContainerTmpDiskMaxBytes,
			PIDsMax:             settings.ContainerPIDsMax,
			ExecUser:            settings.ContainerExecUser,
			DropAllCapabilities: settings.ContainerDropAllCapabilities,
			NoNewPrivileges:     settings.ContainerNoNewPrivileges,
			ReadOnlyRootFS:      settings.ContainerReadOnlyRootFS,
			SeccompProfilePath:  settings.ContainerSeccompProfilePath,
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
	idleDeadline := now.Add(settings.IdleStopTimeout)
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
	return s.view(updated), nil
}

// removeFailedSandbox removes what a failed container with externalID may still have in the
// provider (a stop that failed leaves it running) before its id is reused, so replacing the row
// cannot orphan a sandbox. A pruned row has nothing left.
func (s *Service) removeFailedSandbox(ctx context.Context, externalID string) error {
	row, err := s.repo.GetByExternalID(ctx, externalID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lookup container %q: %w", externalID, err)
	}
	if row.State != StateFailed || row.ProviderRef == nil || *row.ProviderRef == "" {
		return nil
	}
	if err := s.provider.PruneSandbox(ctx, *row.ProviderRef); err != nil && !errors.Is(err, types.ErrProviderNotFound) {
		return fmt.Errorf("%w: remove the failed container before reusing its id: %w", ErrProviderFailure, err)
	}
	return nil
}

// liveContainer is the container with externalID when it exists and is not pruned or failed;
// nil when there is none (or only a terminal row, whose id may be reused).
func (s *Service) liveContainer(ctx context.Context, externalID string) (*ContainerView, error) {
	row, err := s.repo.GetByExternalID(ctx, externalID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lookup container %q: %w", externalID, err)
	}
	if row.State == StatePruned || row.State == StateFailed {
		return nil, nil
	}
	return s.view(row), nil
}

func (s *Service) GetContainer(ctx context.Context, externalID string) (*ContainerView, error) {
	row, err := s.repo.GetByExternalID(ctx, externalID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrContainerNotFound
		}
		return nil, fmt.Errorf("load metadata row: %w", err)
	}

	return s.view(row), nil
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
	retentionDeadline := now.Add(s.cfg.ForImage(row.ImageName).StoppedRetentionTimeout)
	stopped := repository.UpdateStateParams{
		ExternalID:               row.ExternalID,
		State:                    stoppedState,
		SetActivityNow:           true,
		StoppedAt:                &now,
		RetentionPruneDeadlineAt: &retentionDeadline,
		ClearIdleStopDeadline:    true,
	}
	if row.LifecyclePolicy == LifecycleStopOnly {
		// Stop-only: stopped, never pruned by the reconciler, so no retention deadline.
		stopped.RetentionPruneDeadlineAt = nil
		stopped.ClearRetentionPruneDeadline = true
	}
	updated, err := s.repo.UpdateStateIfCurrent(ctx, stopped, stoppingState)
	if err != nil {
		if errors.Is(err, repository.ErrStateConflict) {
			outcome = "state_conflict"
			return nil, fmt.Errorf("%w: stop completion transition race", ErrStateConflict)
		}
		return nil, fmt.Errorf("persist stopped state: %w", err)
	}
	outcome = "success"
	s.refreshContainerCountMetrics(ctx)
	return s.view(updated), nil
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
	idleDeadline := now.Add(s.cfg.ForImage(row.ImageName).IdleStopTimeout)
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
	return s.view(updated), nil
}

func (s *Service) PruneContainer(ctx context.Context, externalID string) (*ContainerView, error) {
	return s.PruneContainerWith(ctx, externalID, PruneOptions{})
}

// PruneContainerWith prunes the container; with Purge, a persistent container's workspace volume
// goes too (without it the volume stays, for a container created again under the same id).
func (s *Service) PruneContainerWith(ctx context.Context, externalID string, opts PruneOptions) (*ContainerView, error) {
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

	err = s.provider.PruneSandbox(ctx, *row.ProviderRef)
	if err == nil && opts.Purge && row.StoragePolicy == StoragePersistent {
		if purger, ok := s.provider.(types.VolumePurger); ok {
			err = purger.RemoveWorkspaceVolume(ctx, row.ExternalID)
		}
	}
	if err != nil {
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
	return s.view(updated), nil
}

// view is c as clients see it, with its image's name and network.
func (s *Service) view(c *repository.Container) *ContainerView {
	v := mapContainer(c)
	if img, ok := s.cfg.Image(c.ImageName); ok {
		v.ImageName, v.Network = img.Name, img.Network
	}
	v.Lifecycle, v.Storage = c.LifecyclePolicy, c.StoragePolicy
	return v
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
