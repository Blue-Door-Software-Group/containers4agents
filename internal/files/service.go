package files

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	ErrDiskQuotaExceeded = errors.New("workspace disk quota exceeded")
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

func (s *Service) Upload(ctx context.Context, externalID string, requestedPath string, content io.Reader) error {
	path, row, providerRef, err := s.resolvePathAndContainer(ctx, externalID, requestedPath)
	if err != nil {
		return err
	}

	usage, err := s.provider.ReadUsage(ctx, providerRef)
	if err != nil {
		return s.mapProviderErr(err)
	}
	if s.cfg.ContainerFileCountMax > 0 && usage.FileCount >= s.cfg.ContainerFileCountMax {
		return fmt.Errorf("%w: file count limit reached", ErrPayloadTooLarge)
	}

	remainingQuota := int64(s.cfg.FileMaxUploadBytes)
	if s.cfg.WorkspaceDiskQuotaBytes > 0 {
		remainingQuota = s.cfg.WorkspaceDiskQuotaBytes - usage.WorkspaceBytes
		if remainingQuota <= 0 {
			return ErrDiskQuotaExceeded
		}
	}
	uploadLimit := int64(s.cfg.FileMaxUploadBytes)
	if remainingQuota < uploadLimit {
		uploadLimit = remainingQuota
	}

	limitedReader := newMaxBytesReader(content, uploadLimit)

	if err := s.provider.UploadFile(ctx, providerRef, path, limitedReader); err != nil {
		if errors.Is(err, errExceededMaxBytes) {
			if uploadLimit == remainingQuota {
				return ErrDiskQuotaExceeded
			}
			return fmt.Errorf("%w: file payload exceeds configured limit", ErrPayloadTooLarge)
		}
		return s.mapProviderErr(err)
	}
	s.touchActivity(ctx, row.ExternalID, row.State)
	return nil
}

func (s *Service) Download(ctx context.Context, externalID string, requestedPath string, dst io.Writer) error {
	path, row, providerRef, err := s.resolvePathAndContainer(ctx, externalID, requestedPath)
	if err != nil {
		return err
	}

	limitedWriter := newMaxBytesWriter(dst, int64(s.cfg.FileMaxDownloadBytes))
	err = s.provider.DownloadFile(ctx, providerRef, path, limitedWriter)
	if err != nil {
		if errors.Is(err, errExceededMaxBytes) {
			return fmt.Errorf("%w: file payload exceeds configured limit", ErrPayloadTooLarge)
		}
		return s.mapProviderErr(err)
	}

	s.touchActivity(ctx, row.ExternalID, row.State)
	return nil
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
	case errors.Is(err, types.ErrProviderQuotaExceeded):
		return ErrDiskQuotaExceeded
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

var errExceededMaxBytes = errors.New("exceeded max bytes")

type maxBytesReader struct {
	reader   io.Reader
	maxBytes int64
	read     int64
}

func newMaxBytesReader(reader io.Reader, maxBytes int64) *maxBytesReader {
	return &maxBytesReader{
		reader:   reader,
		maxBytes: maxBytes,
	}
}

func (r *maxBytesReader) Read(p []byte) (int, error) {
	if r.maxBytes <= 0 {
		return 0, errExceededMaxBytes
	}
	remaining := r.maxBytes - r.read
	if remaining <= 0 {
		return 0, errExceededMaxBytes
	}
	if int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := r.reader.Read(p)
	r.read += int64(n)
	if err == io.EOF && r.read > r.maxBytes {
		return n, errExceededMaxBytes
	}
	return n, err
}

type maxBytesWriter struct {
	writer   io.Writer
	maxBytes int64
	written  int64
}

func newMaxBytesWriter(writer io.Writer, maxBytes int64) *maxBytesWriter {
	return &maxBytesWriter{
		writer:   writer,
		maxBytes: maxBytes,
	}
}

func (w *maxBytesWriter) Write(p []byte) (int, error) {
	if w.maxBytes <= 0 {
		return 0, errExceededMaxBytes
	}
	remaining := w.maxBytes - w.written
	if remaining <= 0 {
		return 0, errExceededMaxBytes
	}
	if int64(len(p)) > remaining {
		p = p[:remaining]
		n, err := w.writer.Write(p)
		w.written += int64(n)
		if err != nil {
			return n, err
		}
		return n, errExceededMaxBytes
	}
	n, err := w.writer.Write(p)
	w.written += int64(n)
	return n, err
}
