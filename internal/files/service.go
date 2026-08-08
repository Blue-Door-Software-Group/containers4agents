package files

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"time"

	"github.com/Blue-Door-Software-Group/containers4agents/internal/config"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/lifecycle"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/metadata/repository"
	obslog "github.com/Blue-Door-Software-Group/containers4agents/internal/observability/logging"
	obsmetrics "github.com/Blue-Door-Software-Group/containers4agents/internal/observability/metrics"
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
	metrics  *obsmetrics.Metrics
}

type Manifest struct {
	ContainerID        string
	Entries            []types.FileEntry
	WorkspaceBytesUsed int64
	WorkspaceDiskQuota int64
	WorkspaceFileCount int64
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

func (s *Service) Upload(ctx context.Context, externalID string, requestedPath string, content io.Reader) error {
	started := time.Now()
	outcome := "internal_error"
	countingReader := &countingReader{reader: content}
	defer func() {
		s.observeFileTransfer("upload", outcome, countingReader.read, time.Since(started))
	}()

	path, row, providerRef, err := s.resolvePathAndContainer(ctx, externalID, requestedPath)
	if err != nil {
		outcome = mapFileOutcome(err)
		return err
	}

	usage, err := s.provider.ReadUsage(ctx, providerRef)
	if err != nil {
		outcome = "provider_failure"
		s.recordProviderFailure("files.read_usage", providerFailureCategory(err))
		return s.mapProviderErr(err)
	}
	if s.cfg.ContainerFileCountMax > 0 && usage.FileCount >= s.cfg.ContainerFileCountMax {
		outcome = "quota"
		return fmt.Errorf("%w: file count limit reached", ErrPayloadTooLarge)
	}

	remainingQuota := int64(s.cfg.FileMaxUploadBytes)
	if s.cfg.WorkspaceDiskQuotaBytes > 0 {
		remainingQuota = s.cfg.WorkspaceDiskQuotaBytes - usage.WorkspaceBytes
		if remainingQuota <= 0 {
			outcome = "quota"
			return ErrDiskQuotaExceeded
		}
	}
	uploadLimit := int64(s.cfg.FileMaxUploadBytes)
	if remainingQuota < uploadLimit {
		uploadLimit = remainingQuota
	}

	limitedReader := newMaxBytesReader(countingReader, uploadLimit)

	if err := s.provider.UploadFile(ctx, providerRef, path, limitedReader); err != nil {
		if errors.Is(err, errExceededMaxBytes) {
			if uploadLimit == remainingQuota {
				outcome = "quota"
				return ErrDiskQuotaExceeded
			}
			outcome = "payload_too_large"
			return fmt.Errorf("%w: file payload exceeds configured limit", ErrPayloadTooLarge)
		}
		outcome = "provider_failure"
		s.recordProviderFailure("files.upload", providerFailureCategory(err))
		return s.mapProviderErr(err)
	}
	s.touchActivity(ctx, row.ExternalID, row.State)
	outcome = "success"
	return nil
}

func (s *Service) Download(ctx context.Context, externalID string, requestedPath string, dst io.Writer) error {
	started := time.Now()
	outcome := "internal_error"
	countingWriter := &countingWriter{writer: dst}
	defer func() {
		s.observeFileTransfer("download", outcome, countingWriter.written, time.Since(started))
	}()

	path, row, providerRef, err := s.resolvePathAndContainer(ctx, externalID, requestedPath)
	if err != nil {
		outcome = mapFileOutcome(err)
		return err
	}

	limitedWriter := newMaxBytesWriter(countingWriter, int64(s.cfg.FileMaxDownloadBytes))
	err = s.provider.DownloadFile(ctx, providerRef, path, limitedWriter)
	if err != nil {
		if errors.Is(err, errExceededMaxBytes) {
			outcome = "payload_too_large"
			return fmt.Errorf("%w: file payload exceeds configured limit", ErrPayloadTooLarge)
		}
		outcome = "provider_failure"
		s.recordProviderFailure("files.download", providerFailureCategory(err))
		return s.mapProviderErr(err)
	}

	s.touchActivity(ctx, row.ExternalID, row.State)
	outcome = "success"
	return nil
}

func (s *Service) Delete(ctx context.Context, externalID string, requestedPath string) error {
	path, row, providerRef, err := s.resolvePathAndContainer(ctx, externalID, requestedPath)
	if err != nil {
		return err
	}

	if err := s.provider.DeletePath(ctx, providerRef, path); err != nil {
		s.recordProviderFailure("files.delete", providerFailureCategory(err))
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
		s.recordProviderFailure("files.list_workspace", providerFailureCategory(err))
		return nil, s.mapProviderErr(err)
	}
	usage, err := s.provider.ReadUsage(ctx, providerRef)
	if err != nil {
		s.recordProviderFailure("files.read_usage", providerFailureCategory(err))
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
	s.observeWorkspaceUsage(usage.WorkspaceBytes, usage.FileCount)
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
	now := time.Now().UTC()
	params := repository.UpdateStateParams{
		ExternalID:     externalID,
		State:          expectedState,
		SetActivityNow: true,
	}
	switch expectedState {
	case lifecycle.StateRunning:
		idleDeadline := now.Add(s.cfg.IdleStopTimeout)
		params.IdleStopDeadlineAt = &idleDeadline
	case lifecycle.StateStopped:
		retentionDeadline := now.Add(s.cfg.StoppedRetentionTimeout)
		params.RetentionPruneDeadlineAt = &retentionDeadline
	}
	_, updateErr := s.repo.UpdateStateIfCurrent(ctx, repository.UpdateStateParams{
		ExternalID:               params.ExternalID,
		State:                    params.State,
		SetActivityNow:           params.SetActivityNow,
		IdleStopDeadlineAt:       params.IdleStopDeadlineAt,
		RetentionPruneDeadlineAt: params.RetentionPruneDeadlineAt,
	}, expectedState)
	if updateErr != nil && !errors.Is(updateErr, repository.ErrStateConflict) && !errors.Is(updateErr, repository.ErrNotFound) {
		obslog.WithRequestID(ctx, slog.Default()).WarnContext(ctx, "failed to persist file-operation activity timestamp", "component", "files", "operation", "touch_activity", "error_category", obslog.ErrorCategory(updateErr))
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

type countingReader struct {
	reader io.Reader
	read   int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.reader.Read(p)
	c.read += int64(n)
	return n, err
}

type countingWriter struct {
	writer  io.Writer
	written int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.writer.Write(p)
	c.written += int64(n)
	return n, err
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

func mapFileOutcome(err error) string {
	switch {
	case err == nil:
		return "success"
	case errors.Is(err, ErrContainerNotFound), errors.Is(err, ErrFileNotFound):
		return "not_found"
	case errors.Is(err, ErrPathViolation):
		return "path_violation"
	case errors.Is(err, ErrPayloadTooLarge):
		return "payload_too_large"
	case errors.Is(err, ErrDiskQuotaExceeded):
		return "quota"
	case errors.Is(err, ErrStateConflict):
		return "state_conflict"
	case errors.Is(err, ErrProviderFailure):
		return "provider_failure"
	default:
		return "internal_error"
	}
}

func (s *Service) recordProviderFailure(operation string, category string) {
	if s.metrics == nil {
		return
	}
	s.metrics.RecordProviderFailure(operation, category)
}

func (s *Service) observeFileTransfer(direction string, outcome string, bytes int64, d time.Duration) {
	if s.metrics == nil {
		return
	}
	s.metrics.ObserveFileTransfer(direction, outcome, bytes, d)
}

func (s *Service) observeWorkspaceUsage(bytes int64, files int64) {
	if s.metrics == nil {
		return
	}
	s.metrics.ObserveWorkspaceUsage(bytes, files)
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
