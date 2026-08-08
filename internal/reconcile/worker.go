package reconcile

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/Blue-Door-Software-Group/containers4agents/internal/lifecycle"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/metadata/repository"
	obslog "github.com/Blue-Door-Software-Group/containers4agents/internal/observability/logging"
	obsmetrics "github.com/Blue-Door-Software-Group/containers4agents/internal/observability/metrics"
)

var ErrPermanentFailure = errors.New("reconcile permanent failure")

type LifecycleService interface {
	StopContainer(ctx context.Context, externalID string) (*lifecycle.ContainerView, error)
	PruneContainer(ctx context.Context, externalID string) (*lifecycle.ContainerView, error)
}

type Worker struct {
	repo             repository.ContainerRepository
	lifecycle        LifecycleService
	logger           *slog.Logger
	enabled          bool
	distributed      bool
	interval         time.Duration
	followerRetry    time.Duration
	idleStopTimeout  time.Duration
	retentionTimeout time.Duration
	lock             *AdvisoryLock
	metrics          *obsmetrics.Metrics
}

func NewWorker(
	db *sql.DB,
	repo repository.ContainerRepository,
	lifecycleSvc LifecycleService,
	logger *slog.Logger,
	enabled bool,
	distributed bool,
	interval time.Duration,
	followerRetry time.Duration,
	idleStopTimeout time.Duration,
	retentionTimeout time.Duration,
	lockKey int64,
	metrics ...*obsmetrics.Metrics,
) *Worker {
	var m *obsmetrics.Metrics
	if len(metrics) > 0 {
		m = metrics[0]
	}
	return &Worker{
		repo:             repo,
		lifecycle:        lifecycleSvc,
		logger:           logger,
		enabled:          enabled,
		distributed:      distributed,
		interval:         interval,
		followerRetry:    followerRetry,
		idleStopTimeout:  idleStopTimeout,
		retentionTimeout: retentionTimeout,
		lock:             NewAdvisoryLock(db, lockKey),
		metrics:          m,
	}
}

func (w *Worker) Run(ctx context.Context) (runErr error) {
	defer func() {
		if r := recover(); r != nil {
			runErr = fmt.Errorf("%w: %v\n%s", ErrPermanentFailure, r, string(debug.Stack()))
		}
	}()

	if w == nil || !w.enabled {
		return nil
	}
	w.setReconcileLeader(false)
	if !w.distributed {
		w.setReconcileLeader(true)
		w.runLeaderLoop(ctx)
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		acquired, err := w.lock.TryAcquire(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			w.recordReconcileAction("lock_acquire", "error")
			obslog.WithRequestID(ctx, w.logger).WarnContext(ctx, "reconcile lock acquire failed", "component", "reconcile", "operation", "lock_acquire", "error_category", obslog.ErrorCategory(err))
			if !w.sleep(ctx, w.followerRetry) {
				return nil
			}
			continue
		}
		if !acquired {
			if !w.sleep(ctx, w.followerRetry) {
				return nil
			}
			continue
		}

		w.logger.Info("reconcile leader acquired")
		w.recordReconcileAction("leader_acquire", "success")
		w.setReconcileLeader(true)
		w.runLeaderLoop(ctx)
		w.setReconcileLeader(false)
		if err := w.lock.Release(context.Background()); err != nil {
			w.recordReconcileAction("lock_release", "error")
			w.logger.Warn("reconcile lock release failed", "component", "reconcile", "operation", "lock_release", "error_category", obslog.ErrorCategory(err))
		}
		if !w.sleep(ctx, w.followerRetry) {
			return nil
		}
	}
}

func (w *Worker) runLeaderLoop(ctx context.Context) {
	w.reconcileOnce(ctx)

	workTicker := time.NewTicker(w.interval)
	defer workTicker.Stop()
	healthTicker := time.NewTicker(w.followerRetry)
	defer healthTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-workTicker.C:
			w.reconcileOnce(ctx)
		case <-healthTicker.C:
			if w.distributed {
				if err := w.lock.HealthError(ctx); err != nil {
					w.logger.Warn("reconcile leader lock unhealthy; demoting", "error", err)
					return
				}
			}
		}
	}
}

func (w *Worker) reconcileOnce(ctx context.Context) {
	w.recordReconcileRun("started")
	now := time.Now().UTC()
	w.backfillLegacyDeadlines(ctx, now)
	w.cleanupExpiredIdempotency(ctx, now)
	w.reconcileExpiredRunningIdle(ctx, now)
	w.reconcileExpiredStoppedRetention(ctx, now)
	w.reconcilePruneRetries(ctx, now)
	w.recordReconcileRun("completed")
}

func (w *Worker) backfillLegacyDeadlines(ctx context.Context, now time.Time) {
	count, err := w.repo.BackfillLifecycleDeadlines(ctx, now, w.idleStopTimeout, w.retentionTimeout, 200)
	if err != nil {
		w.recordReconcileAction("backfill", "error")
		obslog.WithRequestID(ctx, w.logger).WarnContext(ctx, "failed lifecycle deadline backfill", "component", "reconcile", "operation", "backfill", "error_category", obslog.ErrorCategory(err))
		return
	}
	w.recordReconcileAction("backfill", "success")
	if count > 0 {
		w.logger.InfoContext(ctx, "backfilled lifecycle deadlines", "rows", count)
	}
}

func (w *Worker) reconcileExpiredRunningIdle(ctx context.Context, now time.Time) {
	rows, err := w.repo.FindExpiredRunningIdle(ctx, now, 100)
	if err != nil {
		w.recordReconcileAction("query_expired_running_idle", "error")
		obslog.WithRequestID(ctx, w.logger).WarnContext(ctx, "failed to query expired running idle containers", "component", "reconcile", "operation", "query_expired_running_idle", "error_category", obslog.ErrorCategory(err))
		return
	}
	w.recordReconcileAction("query_expired_running_idle", "success")
	for _, row := range rows {
		if _, stopErr := w.lifecycle.StopContainer(ctx, row.ExternalID); stopErr != nil {
			w.recordReconcileAction("stop_idle_expired", "error")
			obslog.WithRequestID(ctx, w.logger).WarnContext(ctx, "failed idle-timeout stop", "component", "reconcile", "operation", "stop_idle_expired", "error_category", obslog.ErrorCategory(stopErr))
		} else {
			w.recordReconcileAction("stop_idle_expired", "success")
		}
	}
}

func (w *Worker) reconcileExpiredStoppedRetention(ctx context.Context, now time.Time) {
	rows, err := w.repo.FindExpiredStoppedRetention(ctx, now, 100)
	if err != nil {
		w.recordReconcileAction("query_expired_stopped_retention", "error")
		obslog.WithRequestID(ctx, w.logger).WarnContext(ctx, "failed to query expired stopped retention containers", "component", "reconcile", "operation", "query_expired_stopped_retention", "error_category", obslog.ErrorCategory(err))
		return
	}
	w.recordReconcileAction("query_expired_stopped_retention", "success")
	for _, row := range rows {
		if _, pruneErr := w.lifecycle.PruneContainer(ctx, row.ExternalID); pruneErr != nil {
			w.recordReconcileAction("prune_retention_expired", "error")
			obslog.WithRequestID(ctx, w.logger).WarnContext(ctx, "failed retention prune", "component", "reconcile", "operation", "prune_retention_expired", "error_category", obslog.ErrorCategory(pruneErr))
		} else {
			w.recordReconcileAction("prune_retention_expired", "success")
		}
	}
}

func (w *Worker) reconcilePruneRetries(ctx context.Context, now time.Time) {
	rows, err := w.repo.FindPruneRetryDue(ctx, now, 100)
	if err != nil {
		w.recordReconcileAction("query_prune_retry_due", "error")
		obslog.WithRequestID(ctx, w.logger).WarnContext(ctx, "failed to query prune retries", "component", "reconcile", "operation", "query_prune_retry_due", "error_category", obslog.ErrorCategory(err))
		return
	}
	w.recordReconcileAction("query_prune_retry_due", "success")
	for _, row := range rows {
		_, _ = w.repo.UpdateStateIfCurrent(ctx, repository.UpdateStateParams{
			ExternalID: row.ExternalID,
			State:      lifecycle.StateStopped,
			StoppedAt:  row.StoppedAt,
		}, lifecycle.StateFailed)
		if _, pruneErr := w.lifecycle.PruneContainer(ctx, row.ExternalID); pruneErr != nil {
			w.recordReconcileAction("prune_retry", "error")
			obslog.WithRequestID(ctx, w.logger).WarnContext(ctx, "failed prune retry", "component", "reconcile", "operation", "prune_retry", "error_category", obslog.ErrorCategory(pruneErr))
		} else {
			w.recordReconcileAction("prune_retry", "success")
		}
	}
}

func (w *Worker) cleanupExpiredIdempotency(ctx context.Context, now time.Time) {
	_, err := w.repo.DeleteExpiredIdempotencyRecords(ctx, now)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		w.recordReconcileAction("cleanup_idempotency", "error")
		obslog.WithRequestID(ctx, w.logger).WarnContext(ctx, "failed to cleanup expired idempotency records", "component", "reconcile", "operation", "cleanup_idempotency", "error_category", obslog.ErrorCategory(err))
		return
	}
	w.recordReconcileAction("cleanup_idempotency", "success")
}

func (w *Worker) sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (w *Worker) recordReconcileRun(outcome string) {
	if w == nil || w.metrics == nil {
		return
	}
	w.metrics.RecordReconcileRun(outcome)
}

func (w *Worker) recordReconcileAction(action string, outcome string) {
	if w == nil || w.metrics == nil {
		return
	}
	w.metrics.RecordReconcileAction(action, outcome)
}

func (w *Worker) setReconcileLeader(leader bool) {
	if w == nil || w.metrics == nil {
		return
	}
	w.metrics.SetReconcileLeader(leader)
}
