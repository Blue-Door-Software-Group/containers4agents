package lifecycle

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/Blue-Door-Software-Group/containers4agents/internal/config"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/metadata/repository"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/provider/types"
)

type fakeRepo struct {
	createFn        func(context.Context, repository.CreateParams) (*repository.Container, error)
	getFn           func(context.Context, string) (*repository.Container, error)
	updateFn        func(context.Context, repository.UpdateStateParams) (*repository.Container, error)
	updateIfCurrent func(context.Context, repository.UpdateStateParams, string) (*repository.Container, error)
	beginExecFn     func(context.Context, string, string) error
	endExecFn       func(context.Context, string) error
	countByStateFn  func(context.Context, string) (int, error)
	countTotalFn    func(context.Context) (int, error)
	countExecFn     func(context.Context) (int, error)
}

func (f fakeRepo) Create(ctx context.Context, params repository.CreateParams) (*repository.Container, error) {
	return f.createFn(ctx, params)
}
func (f fakeRepo) GetByExternalID(ctx context.Context, externalID string) (*repository.Container, error) {
	return f.getFn(ctx, externalID)
}
func (f fakeRepo) UpdateState(ctx context.Context, params repository.UpdateStateParams) (*repository.Container, error) {
	return f.updateFn(ctx, params)
}
func (f fakeRepo) UpdateStateIfCurrent(ctx context.Context, params repository.UpdateStateParams, expected string) (*repository.Container, error) {
	return f.updateIfCurrent(ctx, params, expected)
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
	createErr error
	startErr  error
	stopErr   error
	pruneErr  error
}

func (f fakeProvider) CreateSandbox(context.Context, types.SandboxSpec) (types.ProviderContainer, error) {
	if f.createErr != nil {
		return types.ProviderContainer{}, f.createErr
	}
	return types.ProviderContainer{ProviderRef: "provider-1", State: StateRunning}, nil
}
func (f fakeProvider) StartSandbox(context.Context, string) error { return f.startErr }
func (f fakeProvider) StopSandbox(context.Context, string) error  { return f.stopErr }
func (f fakeProvider) PruneSandbox(context.Context, string) error { return f.pruneErr }
func (f fakeProvider) Exec(context.Context, string, types.ExecRequest) (types.ExecResult, error) {
	return types.ExecResult{}, types.ErrProviderNotImplemented
}
func (f fakeProvider) UploadFile(context.Context, string, string, io.Reader) error {
	return types.ErrProviderNotImplemented
}
func (f fakeProvider) DownloadFile(context.Context, string, string, io.Writer) error {
	return types.ErrProviderNotImplemented
}
func (f fakeProvider) DeletePath(context.Context, string, string) error {
	return types.ErrProviderNotImplemented
}
func (f fakeProvider) ListWorkspace(context.Context, string) ([]types.FileEntry, error) {
	return nil, types.ErrProviderNotImplemented
}
func (f fakeProvider) ReadUsage(context.Context, string) (types.UsageSnapshot, error) {
	return types.UsageSnapshot{}, nil
}

func TestCreateContainerMapsProviderFailure(t *testing.T) {
	now := time.Now().UTC()
	repo := fakeRepo{
		createFn: func(_ context.Context, _ repository.CreateParams) (*repository.Container, error) {
			return &repository.Container{
				ExternalID:   "container-1",
				State:        StateCreating,
				Profile:      "prod-strict",
				ImageVersion: "v1",
				CreatedAt:    now,
				UpdatedAt:    now,
			}, nil
		},
		updateFn: func(_ context.Context, _ repository.UpdateStateParams) (*repository.Container, error) {
			return nil, nil
		},
		updateIfCurrent: func(_ context.Context, _ repository.UpdateStateParams, _ string) (*repository.Container, error) {
			return nil, nil
		},
	}

	svc := NewService(repo, fakeProvider{createErr: errors.New("docker down")}, config.Config{
		DefaultProfile:     "prod-strict",
		ActiveImageVersion: "v1",
		DockerImage:        "alpine:3.20",
	})
	_, err := svc.CreateContainer(context.Background())
	if err == nil {
		t.Fatal("expected provider failure")
	}
	if !errors.Is(err, ErrProviderFailure) {
		t.Fatalf("expected ErrProviderFailure, got %v", err)
	}
}

func TestStartContainerTransitionRaceMapsConflict(t *testing.T) {
	now := time.Now().UTC()
	repo := fakeRepo{
		getFn: func(_ context.Context, _ string) (*repository.Container, error) {
			providerRef := "provider-1"
			return &repository.Container{
				ExternalID:   "container-1",
				State:        StateStopped,
				Profile:      "prod-strict",
				ImageVersion: "v1",
				ProviderRef:  &providerRef,
				CreatedAt:    now,
				UpdatedAt:    now,
			}, nil
		},
		updateIfCurrent: func(_ context.Context, _ repository.UpdateStateParams, _ string) (*repository.Container, error) {
			return nil, repository.ErrStateConflict
		},
	}

	svc := NewService(repo, fakeProvider{}, config.Config{})
	_, err := svc.StartContainer(context.Background(), "container-1")
	if err == nil {
		t.Fatal("expected transition race conflict")
	}
	if !errors.Is(err, ErrStateConflict) {
		t.Fatalf("expected ErrStateConflict, got %v", err)
	}
}

func TestCreateContainerRejectsRunningLimit(t *testing.T) {
	repo := fakeRepo{
		countByStateFn: func(_ context.Context, state string) (int, error) {
			if state != StateRunning {
				t.Fatalf("unexpected state: %s", state)
			}
			return 30, nil
		},
		countTotalFn: func(_ context.Context) (int, error) {
			return 50, nil
		},
		createFn: func(_ context.Context, _ repository.CreateParams) (*repository.Container, error) {
			t.Fatal("create should not be called when running limit reached")
			return nil, nil
		},
	}

	svc := NewService(repo, fakeProvider{}, config.Config{
		RunningContainerLimit: 30,
		TotalContainerLimit:   100,
	})
	_, err := svc.CreateContainer(context.Background())
	if err == nil || !errors.Is(err, ErrStateConflict) {
		t.Fatalf("expected state conflict, got %v", err)
	}
}

func TestCreateContainerRejectsTotalLimit(t *testing.T) {
	repo := fakeRepo{
		countByStateFn: func(_ context.Context, _ string) (int, error) {
			return 10, nil
		},
		countTotalFn: func(_ context.Context) (int, error) {
			return 100, nil
		},
		createFn: func(_ context.Context, _ repository.CreateParams) (*repository.Container, error) {
			t.Fatal("create should not be called when total limit reached")
			return nil, nil
		},
	}

	svc := NewService(repo, fakeProvider{}, config.Config{
		RunningContainerLimit: 30,
		TotalContainerLimit:   100,
	})
	_, err := svc.CreateContainer(context.Background())
	if err == nil || !errors.Is(err, ErrStateConflict) {
		t.Fatalf("expected state conflict, got %v", err)
	}
}
