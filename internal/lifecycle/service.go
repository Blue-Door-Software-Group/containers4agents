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
		_, _ = s.repo.UpdateState(ctx, repository.UpdateStateParams{
			ExternalID: row.ExternalID,
			State:      "failed",
		})
		return nil, err
	}

	updated, err := s.repo.UpdateState(ctx, repository.UpdateStateParams{
		ExternalID:     externalID,
		State:          providerContainer.State,
		ProviderRef:    &providerContainer.ProviderRef,
		SetActivityNow: true,
	})
	if err != nil {
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

	if row.State != "running" {
		return nil, fmt.Errorf("%w: expected running, got %s", ErrStateConflict, row.State)
	}

	if _, err := s.repo.UpdateState(ctx, repository.UpdateStateParams{
		ExternalID: row.ExternalID,
		State:      "stopping",
	}); err != nil {
		return nil, fmt.Errorf("persist stopping state: %w", err)
	}

	if row.ProviderRef == nil || *row.ProviderRef == "" {
		return nil, fmt.Errorf("%w: missing provider reference", ErrStateConflict)
	}
	if err := s.provider.StopSandbox(ctx, *row.ProviderRef); err != nil {
		_, _ = s.repo.UpdateState(ctx, repository.UpdateStateParams{
			ExternalID: row.ExternalID,
			State:      "failed",
		})
		return nil, err
	}

	updated, err := s.repo.UpdateState(ctx, repository.UpdateStateParams{
		ExternalID:     row.ExternalID,
		State:          "stopped",
		SetActivityNow: true,
	})
	if err != nil {
		return nil, fmt.Errorf("persist stopped state: %w", err)
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
