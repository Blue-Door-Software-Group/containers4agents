package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Blue-Door-Software-Group/containers4agents/internal/config"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/metadata/repository"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/provider/types"
)

var (
	ErrContainerNotFound = errors.New("container not found")
	ErrStateConflict     = errors.New("container state conflict")
	ErrProviderFailure   = errors.New("provider failure")
)

type Service struct {
	repo     repository.ContainerRepository
	provider types.ContainerProvider
	cfg      config.Config
}

type ContainerView struct {
	ExternalID     string
	State          string
	Profile        string
	ImageVersion   string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	LastActivityAt *time.Time
}

func NewService(repo repository.ContainerRepository, provider types.ContainerProvider, cfg config.Config) *Service {
	return &Service{
		repo:     repo,
		provider: provider,
		cfg:      cfg,
	}
}

func (s *Service) CreateContainer(ctx context.Context) (*ContainerView, error) {
	externalID := uuid.NewString()
	row, err := s.repo.Create(ctx, repository.CreateParams{
		ExternalID:   externalID,
		Profile:      s.cfg.DefaultProfile,
		ImageVersion: s.cfg.ActiveImageVersion,
	})
	if err != nil {
		return nil, fmt.Errorf("create metadata row: %w", err)
	}

	providerContainer, err := s.provider.CreateSandbox(ctx, types.SandboxSpec{
		ExternalID:   externalID,
		Profile:      s.cfg.DefaultProfile,
		ImageVersion: s.cfg.ActiveImageVersion,
		Image:        s.cfg.DockerImage,
	})
	if err != nil {
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
		return nil, err
	}
	updated, err := s.repo.UpdateStateIfCurrent(ctx, repository.UpdateStateParams{
		ExternalID:     externalID,
		State:          next,
		ProviderRef:    &providerContainer.ProviderRef,
		SetActivityNow: true,
	}, row.State)
	if err != nil {
		if errors.Is(err, repository.ErrStateConflict) {
			return nil, fmt.Errorf("%w: create transition race", ErrStateConflict)
		}
		return nil, fmt.Errorf("persist running state: %w", err)
	}

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
	row, err := s.repo.GetByExternalID(ctx, externalID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrContainerNotFound
		}
		return nil, fmt.Errorf("load metadata row: %w", err)
	}

	stoppingState, err := NextState(row.State, TransitionStopRequested)
	if err != nil {
		return nil, err
	}

	if _, err := s.repo.UpdateStateIfCurrent(ctx, repository.UpdateStateParams{
		ExternalID: row.ExternalID,
		State:      stoppingState,
	}, row.State); err != nil {
		if errors.Is(err, repository.ErrStateConflict) {
			return nil, fmt.Errorf("%w: stop transition race", ErrStateConflict)
		}
		return nil, fmt.Errorf("persist stopping state: %w", err)
	}

	if row.ProviderRef == nil || *row.ProviderRef == "" {
		return nil, fmt.Errorf("%w: missing provider reference", ErrStateConflict)
	}
	if err := s.provider.StopSandbox(ctx, *row.ProviderRef); err != nil {
		_, _ = s.repo.UpdateStateIfCurrent(ctx, repository.UpdateStateParams{
			ExternalID: row.ExternalID,
			State:      StateFailed,
		}, stoppingState)
		return nil, fmt.Errorf("%w: stop sandbox: %w", ErrProviderFailure, err)
	}

	stoppedState, err := NextState(stoppingState, TransitionStopCompleted)
	if err != nil {
		return nil, err
	}
	updated, err := s.repo.UpdateStateIfCurrent(ctx, repository.UpdateStateParams{
		ExternalID:     row.ExternalID,
		State:          stoppedState,
		SetActivityNow: true,
	}, stoppingState)
	if err != nil {
		if errors.Is(err, repository.ErrStateConflict) {
			return nil, fmt.Errorf("%w: stop completion transition race", ErrStateConflict)
		}
		return nil, fmt.Errorf("persist stopped state: %w", err)
	}
	return mapContainer(updated), nil
}

func (s *Service) StartContainer(ctx context.Context, externalID string) (*ContainerView, error) {
	row, err := s.repo.GetByExternalID(ctx, externalID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrContainerNotFound
		}
		return nil, fmt.Errorf("load metadata row: %w", err)
	}
	if row.ProviderRef == nil || *row.ProviderRef == "" {
		return nil, fmt.Errorf("%w: missing provider reference", ErrStateConflict)
	}

	startingState, err := NextState(row.State, TransitionStartRequested)
	if err != nil {
		return nil, err
	}
	if _, err := s.repo.UpdateStateIfCurrent(ctx, repository.UpdateStateParams{
		ExternalID: row.ExternalID,
		State:      startingState,
	}, row.State); err != nil {
		if errors.Is(err, repository.ErrStateConflict) {
			return nil, fmt.Errorf("%w: start transition race", ErrStateConflict)
		}
		return nil, fmt.Errorf("persist starting state: %w", err)
	}

	if err := s.provider.StartSandbox(ctx, *row.ProviderRef); err != nil {
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
		return nil, err
	}
	updated, err := s.repo.UpdateStateIfCurrent(ctx, repository.UpdateStateParams{
		ExternalID:     row.ExternalID,
		State:          runningState,
		SetActivityNow: true,
	}, startingState)
	if err != nil {
		if errors.Is(err, repository.ErrStateConflict) {
			return nil, fmt.Errorf("%w: start completion transition race", ErrStateConflict)
		}
		return nil, fmt.Errorf("persist running state from start: %w", err)
	}
	return mapContainer(updated), nil
}

func (s *Service) PruneContainer(ctx context.Context, externalID string) (*ContainerView, error) {
	row, err := s.repo.GetByExternalID(ctx, externalID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrContainerNotFound
		}
		return nil, fmt.Errorf("load metadata row: %w", err)
	}
	if row.ProviderRef == nil || *row.ProviderRef == "" {
		return nil, fmt.Errorf("%w: missing provider reference", ErrStateConflict)
	}

	pruningState, err := NextState(row.State, TransitionPruneRequested)
	if err != nil {
		return nil, err
	}
	if _, err := s.repo.UpdateStateIfCurrent(ctx, repository.UpdateStateParams{
		ExternalID: row.ExternalID,
		State:      pruningState,
	}, row.State); err != nil {
		if errors.Is(err, repository.ErrStateConflict) {
			return nil, fmt.Errorf("%w: prune transition race", ErrStateConflict)
		}
		return nil, fmt.Errorf("persist pruning state: %w", err)
	}

	if err := s.provider.PruneSandbox(ctx, *row.ProviderRef); err != nil {
		if failedState, stateErr := NextState(pruningState, TransitionPruneFailed); stateErr == nil {
			_, _ = s.repo.UpdateStateIfCurrent(ctx, repository.UpdateStateParams{
				ExternalID: row.ExternalID,
				State:      failedState,
			}, pruningState)
		}
		return nil, fmt.Errorf("%w: prune sandbox: %w", ErrProviderFailure, err)
	}

	prunedState, err := NextState(pruningState, TransitionPruneCompleted)
	if err != nil {
		return nil, err
	}
	updated, err := s.repo.UpdateStateIfCurrent(ctx, repository.UpdateStateParams{
		ExternalID:     row.ExternalID,
		State:          prunedState,
		SetActivityNow: true,
	}, pruningState)
	if err != nil {
		if errors.Is(err, repository.ErrStateConflict) {
			return nil, fmt.Errorf("%w: prune completion transition race", ErrStateConflict)
		}
		return nil, fmt.Errorf("persist pruned state: %w", err)
	}
	return mapContainer(updated), nil
}

func mapContainer(c *repository.Container) *ContainerView {
	return &ContainerView{
		ExternalID:     c.ExternalID,
		State:          c.State,
		Profile:        c.Profile,
		ImageVersion:   c.ImageVersion,
		CreatedAt:      c.CreatedAt,
		UpdatedAt:      c.UpdatedAt,
		LastActivityAt: c.LastActivityAt,
	}
}
