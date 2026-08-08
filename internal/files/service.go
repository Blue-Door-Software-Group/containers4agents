package files

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/Blue-Door-Software-Group/containers4agents/internal/config"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/lifecycle"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/metadata/repository"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/provider/types"
)

var (
	ErrContainerNotFound = errors.New("container not found")
	ErrStateConflict     = errors.New("file operation state conflict")
	ErrProviderFailure   = errors.New("file operation provider failure")
	ErrPathViolation     = errors.New("file path policy violation")
	ErrPayloadTooLarge   = errors.New("file payload too large")
	ErrFileNotFound      = errors.New("file not found")
)

type Service struct {
	repo     repository.ContainerRepository
	provider types.ContainerProvider
	cfg      config.Config
}

type Manifest struct {
	ContainerID        string
	Entries            []types.FileEntry
	WorkspaceBytesUsed int64
	WorkspaceDiskQuota int64
	WorkspaceFileCount int64
}

func NewService(repo repository.ContainerRepository, provider types.ContainerProvider, cfg config.Config) *Service {
	return &Service{
		repo:     repo,
		provider: provider,
		cfg:      cfg,
	}
}

func (s *Service) Upload(ctx context.Context, externalID string, requestedPath string, content []byte) error {
	path, row, providerRef, err := s.resolvePathAndContainer(ctx, externalID, requestedPath)
	if err != nil {
		return err
	}
	if len(content) > s.cfg.FileMaxUploadBytes {
		return fmt.Errorf("%w: payload exceeds C4A_FILE_MAX_UPLOAD_BYTES", ErrPayloadTooLarge)
	}

	if err := s.provider.UploadFile(ctx, providerRef, path, content); err != nil {
		return s.mapProviderErr(err)
	}
	s.touchActivity(ctx, row.ExternalID, row.State)
	return nil
}

func (s *Service) Download(ctx context.Context, externalID string, requestedPath string) ([]byte, error) {
	path, row, providerRef, err := s.resolvePathAndContainer(ctx, externalID, requestedPath)
	if err != nil {
		return nil, err
	}

	content, err := s.provider.DownloadFile(ctx, providerRef, path)
	if err != nil {
		return nil, s.mapProviderErr(err)
	}
	if len(content) > s.cfg.FileMaxDownloadBytes {
		return nil, fmt.Errorf("%w: payload exceeds C4A_FILE_MAX_DOWNLOAD_BYTES", ErrPayloadTooLarge)
	}

	s.touchActivity(ctx, row.ExternalID, row.State)
	return content, nil
}

func (s *Service) Delete(ctx context.Context, externalID string, requestedPath string) error {
	path, row, providerRef, err := s.resolvePathAndContainer(ctx, externalID, requestedPath)
	if err != nil {
		return err
	}

	if err := s.provider.DeletePath(ctx, providerRef, path); err != nil {
		return s.mapProviderErr(err)
	}
	s.touchActivity(ctx, row.ExternalID, row.State)
	return nil
}

func (s *Service) Manifest(ctx context.Context, externalID string) (*Manifest, error) {
	row, providerRef, err := s.resolveContainer(ctx, externalID)
	if err != nil {
		return nil, err
	}

	entries, err := s.provider.ListWorkspace(ctx, providerRef)
	if err != nil {
		return nil, s.mapProviderErr(err)
	}
	usage, err := s.provider.ReadUsage(ctx, providerRef)
	if err != nil {
		return nil, s.mapProviderErr(err)
	}

	slices.SortFunc(entries, func(a, b types.FileEntry) int {
		if a.Path < b.Path {
			return -1
		}
		if a.Path > b.Path {
			return 1
		}
		return 0
	})

	s.touchActivity(ctx, row.ExternalID, row.State)
	return &Manifest{
		ContainerID:        row.ExternalID,
		Entries:            entries,
		WorkspaceBytesUsed: usage.WorkspaceBytes,
		WorkspaceDiskQuota: s.cfg.WorkspaceDiskQuotaBytes,
		WorkspaceFileCount: usage.FileCount,
	}, nil
}

func (s *Service) resolvePathAndContainer(
	ctx context.Context,
	externalID string,
	requestedPath string,
) (string, *repository.Container, string, error) {
	if externalID == "" {
		return "", nil, "", fmt.Errorf("%w: missing container id", ErrPathViolation)
	}
	normalizedPath, err := NormalizeWorkspaceRelativePath(requestedPath)
	if err != nil {
		return "", nil, "", fmt.Errorf("%w: %v", ErrPathViolation, err)
	}

	row, providerRef, err := s.resolveContainer(ctx, externalID)
	if err != nil {
		return "", nil, "", err
	}
	return normalizedPath, row, providerRef, nil
}

func (s *Service) resolveContainer(ctx context.Context, externalID string) (*repository.Container, string, error) {
	row, err := s.repo.GetByExternalID(ctx, externalID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, "", ErrContainerNotFound
		}
		return nil, "", fmt.Errorf("load metadata row: %w", err)
	}
	if row.State != lifecycle.StateRunning && row.State != lifecycle.StateStopped {
		return nil, "", fmt.Errorf("%w: container must be running or stopped (state=%s)", ErrStateConflict, row.State)
	}
	if row.ProviderRef == nil || *row.ProviderRef == "" {
		return nil, "", fmt.Errorf("%w: missing provider reference", ErrStateConflict)
	}

	return row, *row.ProviderRef, nil
}

func (s *Service) mapProviderErr(err error) error {
	switch {
	case errors.Is(err, types.ErrProviderNotFound):
		return ErrFileNotFound
	case errors.Is(err, types.ErrProviderDenied):
		return ErrPathViolation
	case errors.Is(err, types.ErrProviderConflict):
		return ErrStateConflict
	default:
		return fmt.Errorf("%w: %w", ErrProviderFailure, err)
	}
}

func (s *Service) touchActivity(ctx context.Context, externalID string, expectedState string) {
	_, updateErr := s.repo.UpdateStateIfCurrent(ctx, repository.UpdateStateParams{
		ExternalID:     externalID,
		State:          expectedState,
		SetActivityNow: true,
	}, expectedState)
	if updateErr != nil && !errors.Is(updateErr, repository.ErrStateConflict) && !errors.Is(updateErr, repository.ErrNotFound) {
		slog.WarnContext(ctx, "failed to persist file-operation activity timestamp", "external_id", externalID, "error", updateErr)
	}
}
