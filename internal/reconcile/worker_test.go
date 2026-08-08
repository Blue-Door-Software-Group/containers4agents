package reconcile

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Blue-Door-Software-Group/containers4agents/internal/lifecycle"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/metadata/repository"
	obslog "github.com/Blue-Door-Software-Group/containers4agents/internal/observability/logging"
	obsmetrics "github.com/Blue-Door-Software-Group/containers4agents/internal/observability/metrics"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

type fakeRepo struct {
	expiredRunning []*repository.Container
}

func (f fakeRepo) Create(context.Context, repository.CreateParams) (*repository.Container, error) {
	return nil, nil
}
func (f fakeRepo) GetByExternalID(context.Context, string) (*repository.Container, error) {
	return nil, repository.ErrNotFound
}
func (f fakeRepo) UpdateState(context.Context, repository.UpdateStateParams) (*repository.Container, error) {
	return nil, nil
}
func (f fakeRepo) UpdateStateIfCurrent(context.Context, repository.UpdateStateParams, string) (*repository.Container, error) {
	return nil, nil
}
func (f fakeRepo) BeginExecution(context.Context, string, string) error  { return nil }
func (f fakeRepo) EndExecution(context.Context, string) error            { return nil }
func (f fakeRepo) CountByState(context.Context, string) (int, error)     { return 0, nil }
func (f fakeRepo) CountTotal(context.Context) (int, error)               { return 0, nil }
func (f fakeRepo) CountExecutionInProgress(context.Context) (int, error) { return 0, nil }
func (f fakeRepo) FindLongestIdleStopped(context.Context, int) ([]*repository.Container, error) {
	return nil, nil
}
func (f fakeRepo) FindExpiredRunningIdle(context.Context, time.Time, int) ([]*repository.Container, error) {
	return f.expiredRunning, nil
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

type panicLifecycle struct{}

func (p panicLifecycle) StopContainer(context.Context, string) (*lifecycle.ContainerView, error) {
	panic("boom")
}
func (p panicLifecycle) PruneContainer(context.Context, string) (*lifecycle.ContainerView, error) {
	return nil, errors.New("not implemented")
}

type noopLifecycle struct{}

func (n noopLifecycle) StopContainer(context.Context, string) (*lifecycle.ContainerView, error) {
	return nil, nil
}
func (n noopLifecycle) PruneContainer(context.Context, string) (*lifecycle.ContainerView, error) {
	return nil, nil
}

func TestRunRecoversPanicsAsError(t *testing.T) {
	w := &Worker{
		repo:             fakeRepo{expiredRunning: []*repository.Container{{ExternalID: "c1"}}},
		lifecycle:        panicLifecycle{},
		logger:           obslog.New("error"),
		enabled:          true,
		distributed:      false,
		interval:         10 * time.Millisecond,
		followerRetry:    10 * time.Millisecond,
		idleStopTimeout:  time.Minute,
		retentionTimeout: time.Hour,
	}

	err := w.Run(context.Background())
	if err == nil {
		t.Fatal("expected panic to be returned as error")
	}
}

func TestRunExitsOnContextCancel(t *testing.T) {
	w := &Worker{
		repo:             fakeRepo{},
		lifecycle:        noopLifecycle{},
		logger:           obslog.New("error"),
		enabled:          true,
		distributed:      true,
		interval:         50 * time.Millisecond,
		followerRetry:    20 * time.Millisecond,
		idleStopTimeout:  time.Minute,
		retentionTimeout: time.Hour,
		lock:             NewAdvisoryLock(nil, 1),
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.Run(ctx); err != nil {
		t.Fatalf("expected clean cancel exit, got %v", err)
	}
}

func TestReconcileRecordsMetrics(t *testing.T) {
	m := obsmetrics.New()
	w := &Worker{
		repo:             fakeRepo{},
		lifecycle:        noopLifecycle{},
		logger:           obslog.New("error"),
		enabled:          true,
		distributed:      false,
		interval:         time.Second,
		followerRetry:    time.Second,
		idleStopTimeout:  time.Minute,
		retentionTimeout: time.Hour,
		metrics:          m,
	}

	w.reconcileOnce(context.Background())

	if got := testutil.ToFloat64(m.ReconcileRuns.WithLabelValues("started")); got != 1 {
		t.Fatalf("expected started reconcile run metric 1, got %v", got)
	}
	if got := testutil.ToFloat64(m.ReconcileRuns.WithLabelValues("completed")); got != 1 {
		t.Fatalf("expected completed reconcile run metric 1, got %v", got)
	}
	if got := testutil.ToFloat64(m.ReconcileActions.WithLabelValues("cleanup_idempotency", "success")); got != 1 {
		t.Fatalf("expected cleanup success metric 1, got %v", got)
	}
}
