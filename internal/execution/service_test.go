package execution

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/Blue-Door-Software-Group/containers4agents/internal/config"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/lifecycle"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/metadata/repository"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/provider/types"
)

type fakeRepo struct {
	getFn           func(context.Context, string) (*repository.Container, error)
	updateIfCurrent func(context.Context, repository.UpdateStateParams, string) (*repository.Container, error)
	createFn        func(context.Context, repository.CreateParams) (*repository.Container, error)
	updateStateFn   func(context.Context, repository.UpdateStateParams) (*repository.Container, error)
	beginExecFn     func(context.Context, string, string) error
	endExecFn       func(context.Context, string) error
	countByStateFn  func(context.Context, string) (int, error)
	countTotalFn    func(context.Context) (int, error)
	countExecFn     func(context.Context) (int, error)
}

func (f fakeRepo) Create(ctx context.Context, params repository.CreateParams) (*repository.Container, error) {
	if f.createFn == nil {
		return nil, nil
	}
	return f.createFn(ctx, params)
}

func (f fakeRepo) GetByExternalID(ctx context.Context, externalID string) (*repository.Container, error) {
	return f.getFn(ctx, externalID)
}

func (f fakeRepo) UpdateState(ctx context.Context, params repository.UpdateStateParams) (*repository.Container, error) {
	if f.updateStateFn == nil {
		return nil, nil
	}
	return f.updateStateFn(ctx, params)
}

func (f fakeRepo) UpdateStateIfCurrent(
	ctx context.Context,
	params repository.UpdateStateParams,
	expectedCurrentState string,
) (*repository.Container, error) {
	return f.updateIfCurrent(ctx, params, expectedCurrentState)
}
func (f fakeRepo) BeginExecution(ctx context.Context, externalID string, expectedState string) error {
	if f.beginExecFn == nil {
		return nil
	}
	return f.beginExecFn(ctx, externalID, expectedState)
}
func (f fakeRepo) EndExecution(ctx context.Context, externalID string) error {
	if f.endExecFn == nil {
		return nil
	}
	return f.endExecFn(ctx, externalID)
}
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

type fakeProvider struct {
	execFn func(context.Context, string, types.ExecRequest) (types.ExecResult, error)
}

func (f fakeProvider) CreateSandbox(context.Context, types.SandboxSpec) (types.ProviderContainer, error) {
	return types.ProviderContainer{}, nil
}
func (f fakeProvider) StartSandbox(context.Context, string) error { return nil }
func (f fakeProvider) StopSandbox(context.Context, string) error  { return nil }
func (f fakeProvider) PruneSandbox(context.Context, string) error { return nil }
func (f fakeProvider) Exec(ctx context.Context, providerRef string, req types.ExecRequest) (types.ExecResult, error) {
	return f.execFn(ctx, providerRef, req)
}
func (f fakeProvider) UploadFile(context.Context, string, string, io.Reader) error   { return nil }
func (f fakeProvider) DownloadFile(context.Context, string, string, io.Writer) error { return nil }
func (f fakeProvider) DeletePath(context.Context, string, string) error              { return nil }
func (f fakeProvider) ListWorkspace(context.Context, string) ([]types.FileEntry, error) {
	return nil, nil
}
func (f fakeProvider) ReadUsage(context.Context, string) (types.UsageSnapshot, error) {
	return types.UsageSnapshot{}, nil
}

func TestExecuteRejectsNonRunningState(t *testing.T) {
	providerRef := "container-ref"
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
				return nil, nil
			},
		},
		fakeProvider{
			execFn: func(context.Context, string, types.ExecRequest) (types.ExecResult, error) {
				t.Fatal("provider exec should not be called when container not running")
				return types.ExecResult{}, nil
			},
		},
		config.Config{ExecTimeout: 2 * time.Second, ExecMaxStdoutBytes: 128, ExecMaxStderrBytes: 128},
	)

	_, err := svc.Execute(context.Background(), "c1", []string{"echo", "hello"})
	if err == nil || !errors.Is(err, ErrStateConflict) {
		t.Fatalf("expected ErrStateConflict, got %v", err)
	}
}

func TestExecuteMapsRepositoryNotFound(t *testing.T) {
	svc := NewService(
		fakeRepo{
			getFn: func(_ context.Context, _ string) (*repository.Container, error) {
				return nil, repository.ErrNotFound
			},
			updateIfCurrent: func(_ context.Context, _ repository.UpdateStateParams, _ string) (*repository.Container, error) {
				return nil, nil
			},
		},
		fakeProvider{
			execFn: func(context.Context, string, types.ExecRequest) (types.ExecResult, error) {
				return types.ExecResult{}, nil
			},
		},
		config.Config{ExecTimeout: 2 * time.Second, ExecMaxStdoutBytes: 128, ExecMaxStderrBytes: 128},
	)

	_, err := svc.Execute(context.Background(), "missing", []string{"echo", "hello"})
	if err == nil || !errors.Is(err, ErrContainerNotFound) {
		t.Fatalf("expected ErrContainerNotFound, got %v", err)
	}
}

func TestExecuteRejectsConcurrentCalls(t *testing.T) {
	providerRef := "provider-1"
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once

	svc := NewService(
		fakeRepo{
			getFn: func(_ context.Context, _ string) (*repository.Container, error) {
				return &repository.Container{
					ExternalID:  "c1",
					State:       lifecycle.StateRunning,
					ProviderRef: &providerRef,
				}, nil
			},
			updateIfCurrent: func(_ context.Context, _ repository.UpdateStateParams, _ string) (*repository.Container, error) {
				return &repository.Container{}, nil
			},
			beginExecFn: func(_ context.Context, _ string, _ string) error {
				return nil
			},
			endExecFn: func(_ context.Context, _ string) error {
				return nil
			},
		},
		fakeProvider{
			execFn: func(_ context.Context, _ string, _ types.ExecRequest) (types.ExecResult, error) {
				once.Do(func() { close(started) })
				<-release
				code := 0
				return types.ExecResult{Status: StatusOK, ExitCode: &code}, nil
			},
		},
		config.Config{ExecTimeout: 3 * time.Second, ExecMaxStdoutBytes: 128, ExecMaxStderrBytes: 128},
	)

	lockHeld := false
	lockMu := sync.Mutex{}
	svc.repo = fakeRepo{
		getFn: func(_ context.Context, _ string) (*repository.Container, error) {
			return &repository.Container{
				ExternalID:  "c1",
				State:       lifecycle.StateRunning,
				ProviderRef: &providerRef,
			}, nil
		},
		updateIfCurrent: func(_ context.Context, _ repository.UpdateStateParams, _ string) (*repository.Container, error) {
			return &repository.Container{}, nil
		},
		beginExecFn: func(_ context.Context, _ string, _ string) error {
			lockMu.Lock()
			defer lockMu.Unlock()
			if lockHeld {
				return repository.ErrExecutionInProgress
			}
			lockHeld = true
			return nil
		},
		endExecFn: func(_ context.Context, _ string) error {
			lockMu.Lock()
			defer lockMu.Unlock()
			lockHeld = false
			return nil
		},
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := svc.Execute(context.Background(), "c1", []string{"echo", "one"})
		errCh <- err
	}()

	<-started

	_, err := svc.Execute(context.Background(), "c1", []string{"echo", "two"})
	if err == nil || !errors.Is(err, ErrStateConflict) {
		t.Fatalf("expected ErrStateConflict on concurrent exec, got %v", err)
	}

	close(release)
	if err := <-errCh; err != nil {
		t.Fatalf("first execution unexpectedly failed: %v", err)
	}
}

func TestExecuteNormalizesUnknownStatusByExitCode(t *testing.T) {
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
			updateIfCurrent: func(_ context.Context, _ repository.UpdateStateParams, _ string) (*repository.Container, error) {
				return &repository.Container{}, nil
			},
		},
		fakeProvider{
			execFn: func(_ context.Context, _ string, _ types.ExecRequest) (types.ExecResult, error) {
				code := 5
				return types.ExecResult{
					Status:   "unrecognized-status",
					ExitCode: &code,
				}, nil
			},
		},
		config.Config{ExecTimeout: 2 * time.Second, ExecMaxStdoutBytes: 128, ExecMaxStderrBytes: 128},
	)

	result, err := svc.Execute(context.Background(), "c1", []string{"false"})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if result.Status != StatusUserError {
		t.Fatalf("expected status %q, got %q", StatusUserError, result.Status)
	}
}

func TestExecuteReturnsResultWhenActivityUpdateFails(t *testing.T) {
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
			updateIfCurrent: func(_ context.Context, _ repository.UpdateStateParams, _ string) (*repository.Container, error) {
				return nil, errors.New("db temporarily unavailable")
			},
		},
		fakeProvider{
			execFn: func(_ context.Context, _ string, _ types.ExecRequest) (types.ExecResult, error) {
				code := 0
				return types.ExecResult{
					Status:   StatusOK,
					ExitCode: &code,
					Stdout:   "done",
				}, nil
			},
		},
		config.Config{ExecTimeout: 2 * time.Second, ExecMaxStdoutBytes: 128, ExecMaxStderrBytes: 128},
	)

	result, err := svc.Execute(context.Background(), "c1", []string{"echo", "done"})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if result == nil {
		t.Fatal("expected execution result")
	}
	if result.Status != StatusOK {
		t.Fatalf("expected status %q, got %q", StatusOK, result.Status)
	}
	if result.Stdout != "done" {
		t.Fatalf("expected stdout %q, got %q", "done", result.Stdout)
	}
}

func TestExecuteRejectsWhenActiveExecutionLimitReached(t *testing.T) {
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
			updateIfCurrent: func(_ context.Context, _ repository.UpdateStateParams, _ string) (*repository.Container, error) {
				return &repository.Container{}, nil
			},
			countExecFn: func(_ context.Context) (int, error) {
				return 10, nil
			},
		},
		fakeProvider{
			execFn: func(_ context.Context, _ string, _ types.ExecRequest) (types.ExecResult, error) {
				t.Fatal("provider should not be called when active limit exceeded")
				return types.ExecResult{}, nil
			},
		},
		config.Config{
			ActiveExecLimit:    10,
			ExecTimeout:        2 * time.Second,
			ExecMaxStdoutBytes: 128,
			ExecMaxStderrBytes: 128,
		},
	)

	_, err := svc.Execute(context.Background(), "c1", []string{"echo", "hello"})
	if err == nil || !errors.Is(err, ErrStateConflict) {
		t.Fatalf("expected ErrStateConflict, got %v", err)
	}
}

func TestNormalizeStatusRecognizesPhase5Statuses(t *testing.T) {
	tests := []string{StatusOOM, StatusDiskQuota, StatusSandboxViolation}
	for _, status := range tests {
		status := status
		t.Run(status, func(t *testing.T) {
			got := normalizeStatus(status, nil)
			if got != status {
				t.Fatalf("expected %q, got %q", status, got)
			}
		})
	}
}
