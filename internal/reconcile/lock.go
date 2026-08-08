package reconcile

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type AdvisoryLock struct {
	db   *sql.DB
	key  int64
	conn *sql.Conn
}

func NewAdvisoryLock(db *sql.DB, key int64) *AdvisoryLock {
	return &AdvisoryLock{db: db, key: key}
}

func (l *AdvisoryLock) TryAcquire(ctx context.Context) (bool, error) {
	if l == nil || l.db == nil {
		return false, errors.New("advisory lock requires database handle")
	}
	conn, err := l.db.Conn(ctx)
	if err != nil {
		return false, fmt.Errorf("open lock connection: %w", err)
	}

	var acquired bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", l.key).Scan(&acquired); err != nil {
		_ = conn.Close()
		return false, fmt.Errorf("acquire advisory lock: %w", err)
	}
	if !acquired {
		_ = conn.Close()
		return false, nil
	}
	l.conn = conn
	return true, nil
}

func (l *AdvisoryLock) IsHealthy(ctx context.Context) bool {
	if l == nil || l.conn == nil {
		return false
	}
	return l.conn.PingContext(ctx) == nil
}

func (l *AdvisoryLock) HealthError(ctx context.Context) error {
	if l == nil || l.conn == nil {
		return errors.New("advisory lock has no active connection")
	}
	if err := l.conn.PingContext(ctx); err != nil {
		_ = l.conn.Close()
		l.conn = nil
		return fmt.Errorf("advisory lock health check failed: %w", err)
	}
	return nil
}

func (l *AdvisoryLock) Release(ctx context.Context) error {
	if l == nil || l.conn == nil {
		return nil
	}

	var released bool
	if err := l.conn.QueryRowContext(ctx, "SELECT pg_advisory_unlock($1)", l.key).Scan(&released); err != nil {
		_ = l.conn.Close()
		l.conn = nil
		return fmt.Errorf("release advisory lock: %w", err)
	}
	closeErr := l.conn.Close()
	l.conn = nil
	if !released {
		return errors.New("advisory lock was not released")
	}
	if closeErr != nil {
		return fmt.Errorf("close advisory lock connection: %w", closeErr)
	}
	return nil
}
