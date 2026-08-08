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
) *Worker {
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
	if !w.distributed {
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
			w.logger.WarnContext(ctx, "reconcile lock acquire failed", "error", err)
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
		w.runLeaderLoop(ctx)
		if err := w.lock.Release(context.Background()); err != nil {
			w.logger.Warn("reconcile lock release failed", "error", err)
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
	now := time.Now().UTC()
	w.backfillLegacyDeadlines(ctx, now)
	w.cleanupExpiredIdempotency(ctx, now)
	w.reconcileExpiredRunningIdle(ctx, now)
	w.reconcileExpiredStoppedRetention(ctx, now)
	w.reconcilePruneRetries(ctx, now)
}

func (w *Worker) backfillLegacyDeadlines(ctx context.Context, now time.Time) {
	count, err := w.repo.BackfillLifecycleDeadlines(ctx, now, w.idleStopTimeout, w.retentionTimeout, 200)
	if err != nil {
		w.logger.WarnContext(ctx, "failed lifecycle deadline backfill", "error", err)
		return
	}
	if count > 0 {
		w.logger.InfoContext(ctx, "backfilled lifecycle deadlines", "rows", count)
	}
}

func (w *Worker) reconcileExpiredRunningIdle(ctx context.Context, now time.Time) {
	rows, err := w.repo.FindExpiredRunningIdle(ctx, now, 100)
	if err != nil {
		w.logger.WarnContext(ctx, "failed to query expired running idle containers", "error", err)
		return
	}
	for _, row := range rows {
		if _, stopErr := w.lifecycle.StopContainer(ctx, row.ExternalID); stopErr != nil {
			w.logger.WarnContext(ctx, "failed idle-timeout stop", "external_id", row.ExternalID, "error", stopErr)
		}
	}
}

func (w *Worker) reconcileExpiredStoppedRetention(ctx context.Context, now time.Time) {
	rows, err := w.repo.FindExpiredStoppedRetention(ctx, now, 100)
	if err != nil {
		w.logger.WarnContext(ctx, "failed to query expired stopped retention containers", "error", err)
		return
	}
	for _, row := range rows {
		if _, pruneErr := w.lifecycle.PruneContainer(ctx, row.ExternalID); pruneErr != nil {
			w.logger.WarnContext(ctx, "failed retention prune", "external_id", row.ExternalID, "error", pruneErr)
		}
	}
}

func (w *Worker) reconcilePruneRetries(ctx context.Context, now time.Time) {
	rows, err := w.repo.FindPruneRetryDue(ctx, now, 100)
	if err != nil {
		w.logger.WarnContext(ctx, "failed to query prune retries", "error", err)
		return
	}
	for _, row := range rows {
		_, _ = w.repo.UpdateStateIfCurrent(ctx, repository.UpdateStateParams{
			ExternalID: row.ExternalID,
			State:      lifecycle.StateStopped,
			StoppedAt:  row.StoppedAt,
		}, lifecycle.StateFailed)
		if _, pruneErr := w.lifecycle.PruneContainer(ctx, row.ExternalID); pruneErr != nil {
			w.logger.WarnContext(ctx, "failed prune retry", "external_id", row.ExternalID, "error", pruneErr)
		}
	}
}

func (w *Worker) cleanupExpiredIdempotency(ctx context.Context, now time.Time) {
	_, err := w.repo.DeleteExpiredIdempotencyRecords(ctx, now)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		w.logger.WarnContext(ctx, "failed to cleanup expired idempotency records", "error", err)
	}
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
