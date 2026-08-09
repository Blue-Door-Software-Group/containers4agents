package files

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Blue-Door-Software-Group/containers4agents/internal/config"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/lifecycle"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/metadata/repository"
	obslog "github.com/Blue-Door-Software-Group/containers4agents/internal/observability/logging"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/provider/types"
)

type fakeRepo struct {
	getFn           func(context.Context, string) (*repository.Container, error)
	updateIfCurrent func(context.Context, repository.UpdateStateParams, string) (*repository.Container, error)
	countByStateFn  func(context.Context, string) (int, error)
	countTotalFn    func(context.Context) (int, error)
	countExecFn     func(context.Context) (int, error)
}

func (f fakeRepo) Create(context.Context, repository.CreateParams) (*repository.Container, error) {
	return nil, nil
}
func (f fakeRepo) GetByExternalID(ctx context.Context, externalID string) (*repository.Container, error) {
	return f.getFn(ctx, externalID)
}
func (f fakeRepo) UpdateState(context.Context, repository.UpdateStateParams) (*repository.Container, error) {
	return nil, nil
}
func (f fakeRepo) UpdateStateIfCurrent(
	ctx context.Context,
	params repository.UpdateStateParams,
	expectedCurrentState string,
) (*repository.Container, error) {
	if f.updateIfCurrent == nil {
		return &repository.Container{}, nil
	}
	return f.updateIfCurrent(ctx, params, expectedCurrentState)
}
func (f fakeRepo) BeginExecution(context.Context, string, string) error { return nil }
func (f fakeRepo) EndExecution(context.Context, string) error           { return nil }
func (f fakeRepo) CountByState(ctx context.Context, state string) (int, error) {
	if f.countByStateFn == nil {
		return 0, nil
	}
	return f.countByStateFn(ctx, state)
}
func (f fakeRepo) CountTotal(ctx context.Context) (int, error) {
	if f.countTotalFn == nil {
		return 0, nil
	}
	return f.countTotalFn(ctx)
}
func (f fakeRepo) CountExecutionInProgress(ctx context.Context) (int, error) {
	if f.countExecFn == nil {
		return 0, nil
	}
	return f.countExecFn(ctx)
}
func (f fakeRepo) FindLongestIdleStopped(context.Context, int) ([]*repository.Container, error) {
	return nil, nil
}
func (f fakeRepo) FindExpiredRunningIdle(context.Context, time.Time, int) ([]*repository.Container, error) {
	return nil, nil
}
func (f fakeRepo) FindExpiredStoppedRetention(context.Context, time.Time, int) ([]*repository.Container, error) {
	return nil, nil
}
func (f fakeRepo) FindLongestIdleRunningNoExec(context.Context, int) ([]*repository.Container, error) {
	return nil, nil
}
func (f fakeRepo) FindPruneRetryDue(context.Context, time.Time, int) ([]*repository.Container, error) {
	return nil, nil
}
func (f fakeRepo) ListNonTerminal(context.Context, int) ([]*repository.Container, error) {
	return nil, nil
}
func (f fakeRepo) BackfillLifecycleDeadlines(context.Context, time.Time, time.Duration, time.Duration, int) (int, error) {
	return 0, nil
}
func (f fakeRepo) GetIdempotencyRecord(context.Context, string, string, string) (*repository.IdempotencyRecord, error) {
	return nil, repository.ErrNotFound
}
func (f fakeRepo) CreateIdempotencyRecord(context.Context, repository.IdempotencyRecord) (*repository.IdempotencyRecord, error) {
	return nil, nil
}
func (f fakeRepo) UpdateIdempotencyRecordResult(context.Context, string, string, string, string, int, string) (*repository.IdempotencyRecord, error) {
	return nil, nil
}
func (f fakeRepo) DeleteExpiredIdempotencyRecords(context.Context, time.Time) (int, error) {
	return 0, nil
}

type fakeProvider struct {
	uploadFn        func(context.Context, string, string, io.Reader) error
	downloadFn      func(context.Context, string, string, io.Writer) error
	deleteFn        func(context.Context, string, string) error
	listWorkspaceFn func(context.Context, string) ([]types.FileEntry, error)
	readUsageFn     func(context.Context, string) (types.UsageSnapshot, error)
}

func (f fakeProvider) CreateSandbox(context.Context, types.SandboxSpec) (types.ProviderContainer, error) {
	return types.ProviderContainer{}, nil
}
func (f fakeProvider) StartSandbox(context.Context, string) error { return nil }
func (f fakeProvider) StopSandbox(context.Context, string) error  { return nil }
func (f fakeProvider) PruneSandbox(context.Context, string) error { return nil }
func (f fakeProvider) Exec(context.Context, string, types.ExecRequest) (types.ExecResult, error) {
	return types.ExecResult{}, nil
}
func (f fakeProvider) UploadFile(ctx context.Context, providerRef string, path string, content io.Reader) error {
	return f.uploadFn(ctx, providerRef, path, content)
}
func (f fakeProvider) DownloadFile(ctx context.Context, providerRef string, path string, dst io.Writer) error {
	return f.downloadFn(ctx, providerRef, path, dst)
}
func (f fakeProvider) DeletePath(ctx context.Context, providerRef string, path string) error {
	return f.deleteFn(ctx, providerRef, path)
}
func (f fakeProvider) ListWorkspace(ctx context.Context, providerRef string) ([]types.FileEntry, error) {
	return f.listWorkspaceFn(ctx, providerRef)
}
func (f fakeProvider) ReadUsage(ctx context.Context, providerRef string) (types.UsageSnapshot, error) {
	return f.readUsageFn(ctx, providerRef)
}

func TestUploadRejectsInvalidState(t *testing.T) {
	providerRef := "provider-1"
	svc := NewService(
		fakeRepo{
			getFn: func(_ context.Context, _ string) (*repository.Container, error) {
				return &repository.Container{
					ExternalID:  "c1",
					State:       lifecycle.StatePruned,
					ProviderRef: &providerRef,
				}, nil
			},
		},
		fakeProvider{
			uploadFn:        func(context.Context, string, string, io.Reader) error { return nil },
			downloadFn:      func(context.Context, string, string, io.Writer) error { return nil },
			deleteFn:        func(context.Context, string, string) error { return nil },
			listWorkspaceFn: func(context.Context, string) ([]types.FileEntry, error) { return nil, nil },
			readUsageFn:     func(context.Context, string) (types.UsageSnapshot, error) { return types.UsageSnapshot{}, nil },
		},
		config.Config{FileMaxUploadBytes: 10, FileMaxDownloadBytes: 10, WorkspaceDiskQuotaBytes: 1024},
	)

	err := svc.Upload(context.Background(), "c1", "a.txt", bytes.NewReader([]byte("x")))
	if err == nil || !errors.Is(err, ErrStateConflict) {
		t.Fatalf("expected ErrStateConflict, got %v", err)
	}
}

func TestDownloadEnforcesMaxBytes(t *testing.T) {
	providerRef := "provider-1"
	svc := NewService(
		fakeRepo{
			getFn: func(_ context.Context, _ string) (*repository.Container, error) {
				return &repository.Container{
					ExternalID:  "c1",
					State:       lifecycle.StateRunning,
					ProviderRef: &providerRef,
				}, nil
			},
		},
		fakeProvider{
			uploadFn: func(context.Context, string, string, io.Reader) error { return nil },
			downloadFn: func(_ context.Context, _ string, _ string, dst io.Writer) error {
				_, err := dst.Write([]byte("toolong"))
				return err
			},
			deleteFn:        func(context.Context, string, string) error { return nil },
			listWorkspaceFn: func(context.Context, string) ([]types.FileEntry, error) { return nil, nil },
			readUsageFn:     func(context.Context, string) (types.UsageSnapshot, error) { return types.UsageSnapshot{}, nil },
		},
		config.Config{FileMaxUploadBytes: 10, FileMaxDownloadBytes: 3, WorkspaceDiskQuotaBytes: 1024},
	)

	var out bytes.Buffer
	err := svc.Download(context.Background(), "c1", "a.txt", &out)
	if err == nil || !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("expected ErrPayloadTooLarge, got %v", err)
	}
}

func TestManifestReturnsSortedEntriesAndUsage(t *testing.T) {
	providerRef := "provider-1"
	svc := NewService(
		fakeRepo{
			getFn: func(_ context.Context, _ string) (*repository.Container, error) {
				return &repository.Container{
					ExternalID:  "c1",
					State:       lifecycle.StateStopped,
					ProviderRef: &providerRef,
				}, nil
			},
			updateIfCurrent: func(_ context.Context, _ repository.UpdateStateParams, _ string) (*repository.Container, error) {
				return nil, errors.New("update failed")
			},
		},
		fakeProvider{
			uploadFn:   func(context.Context, string, string, io.Reader) error { return nil },
			downloadFn: func(context.Context, string, string, io.Writer) error { return nil },
			deleteFn:   func(context.Context, string, string) error { return nil },
			listWorkspaceFn: func(context.Context, string) ([]types.FileEntry, error) {
				return []types.FileEntry{
					{Path: "z.txt", Name: "z.txt", Type: "file", SizeBytes: 1, ModTime: time.Unix(100, 0)},
					{Path: "a.txt", Name: "a.txt", Type: "file", SizeBytes: 1, ModTime: time.Unix(100, 0)},
				}, nil
			},
			readUsageFn: func(context.Context, string) (types.UsageSnapshot, error) {
				return types.UsageSnapshot{WorkspaceBytes: 2, FileCount: 2}, nil
			},
		},
		config.Config{FileMaxUploadBytes: 10, FileMaxDownloadBytes: 10, WorkspaceDiskQuotaBytes: 2048},
	)

	result, err := svc.Manifest(context.Background(), "c1")
	if err != nil {
		t.Fatalf("expected manifest success, got %v", err)
	}
	if len(result.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(result.Entries))
	}
	if result.Entries[0].Path != "a.txt" {
		t.Fatalf("expected entries sorted by path, got first path %q", result.Entries[0].Path)
	}
	if result.WorkspaceBytesUsed != 2 {
		t.Fatalf("expected workspace bytes 2, got %d", result.WorkspaceBytesUsed)
	}
	if result.WorkspaceDiskQuota != 2048 {
		t.Fatalf("expected workspace quota 2048, got %d", result.WorkspaceDiskQuota)
	}
}

func TestUploadRejectsWhenFileCountLimitReached(t *testing.T) {
	providerRef := "provider-1"
	svc := NewService(
		fakeRepo{
			getFn: func(_ context.Context, _ string) (*repository.Container, error) {
				return &repository.Container{
					ExternalID:  "c1",
					State:       lifecycle.StateRunning,
					ProviderRef: &providerRef,
				}, nil
			},
		},
		fakeProvider{
			uploadFn: func(context.Context, string, string, io.Reader) error {
				t.Fatal("upload should not be called when file-count limit reached")
				return nil
			},
			downloadFn:      func(context.Context, string, string, io.Writer) error { return nil },
			deleteFn:        func(context.Context, string, string) error { return nil },
			listWorkspaceFn: func(context.Context, string) ([]types.FileEntry, error) { return nil, nil },
			readUsageFn: func(context.Context, string) (types.UsageSnapshot, error) {
				return types.UsageSnapshot{WorkspaceBytes: 1, FileCount: 100}, nil
			},
		},
		config.Config{
			FileMaxUploadBytes:      10,
			FileMaxDownloadBytes:    10,
			WorkspaceDiskQuotaBytes: 1024,
			ContainerFileCountMax:   100,
		},
	)

	err := svc.Upload(context.Background(), "c1", "a.txt", bytes.NewReader([]byte("x")))
	if err == nil || !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("expected ErrPayloadTooLarge, got %v", err)
	}
}

func TestUploadRejectsWhenWorkspaceQuotaExceeded(t *testing.T) {
	providerRef := "provider-1"
	svc := NewService(
		fakeRepo{
			getFn: func(_ context.Context, _ string) (*repository.Container, error) {
				return &repository.Container{
					ExternalID:  "c1",
					State:       lifecycle.StateRunning,
					ProviderRef: &providerRef,
				}, nil
			},
		},
		fakeProvider{
			uploadFn:        func(context.Context, string, string, io.Reader) error { return nil },
			downloadFn:      func(context.Context, string, string, io.Writer) error { return nil },
			deleteFn:        func(context.Context, string, string) error { return nil },
			listWorkspaceFn: func(context.Context, string) ([]types.FileEntry, error) { return nil, nil },
			readUsageFn: func(context.Context, string) (types.UsageSnapshot, error) {
				return types.UsageSnapshot{WorkspaceBytes: 1024, FileCount: 10}, nil
			},
		},
		config.Config{
			FileMaxUploadBytes:      10,
			FileMaxDownloadBytes:    10,
			WorkspaceDiskQuotaBytes: 1024,
			ContainerFileCountMax:   100,
		},
	)

	err := svc.Upload(context.Background(), "c1", "a.txt", bytes.NewReader([]byte("x")))
	if err == nil || !errors.Is(err, ErrDiskQuotaExceeded) {
		t.Fatalf("expected ErrDiskQuotaExceeded, got %v", err)
	}
}

func TestManifestProviderFailureLogsRawErrorInDevMode(t *testing.T) {
	var logs bytes.Buffer
	oldDefault := slog.Default()
	slog.SetDefault(obslog.NewWithWriter("info", &logs))
	defer slog.SetDefault(oldDefault)

	providerRef := "provider-1"
	svc := NewService(
		fakeRepo{
			getFn: func(_ context.Context, _ string) (*repository.Container, error) {
				return &repository.Container{
					ExternalID:  "c1",
					State:       lifecycle.StateRunning,
					ProviderRef: &providerRef,
				}, nil
			},
		},
		fakeProvider{
			uploadFn:   func(context.Context, string, string, io.Reader) error { return nil },
			downloadFn: func(context.Context, string, string, io.Writer) error { return nil },
			deleteFn:   func(context.Context, string, string) error { return nil },
			listWorkspaceFn: func(context.Context, string) ([]types.FileEntry, error) {
				return nil, errors.New("exec failed with sensitive path=/workspace/secret.txt")
			},
			readUsageFn: func(context.Context, string) (types.UsageSnapshot, error) {
				return types.UsageSnapshot{}, nil
			},
		},
		config.Config{
			DevMode:                 true,
			FileMaxUploadBytes:      10,
			FileMaxDownloadBytes:    10,
			WorkspaceDiskQuotaBytes: 1024,
		},
	)

	_, err := svc.Manifest(context.Background(), "c1")
	if err == nil {
		t.Fatal("expected manifest error")
	}
	out := logs.String()
	if !strings.Contains(out, "file provider call failed") {
		t.Fatalf("expected provider failure log entry, got %q", out)
	}
	if !strings.Contains(out, "\"provider_error\":\"exec failed with sensitive path=/workspace/secret.txt\"") {
		t.Fatalf("expected raw provider error in dev mode logs, got %q", out)
	}
}
