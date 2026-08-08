package idempotency

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lib/pq"

	"github.com/Blue-Door-Software-Group/containers4agents/internal/metadata/repository"
)

type fakeRepo struct {
	records   map[string]*repository.IdempotencyRecord
	createErr error
}

const testHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func (f *fakeRepo) key(op, scope, key string) string { return op + "|" + scope + "|" + key }

func (f *fakeRepo) Create(context.Context, repository.CreateParams) (*repository.Container, error) {
	return nil, nil
}
func (f *fakeRepo) GetByExternalID(context.Context, string) (*repository.Container, error) {
	return nil, nil
}
func (f *fakeRepo) UpdateState(context.Context, repository.UpdateStateParams) (*repository.Container, error) {
	return nil, nil
}
func (f *fakeRepo) UpdateStateIfCurrent(context.Context, repository.UpdateStateParams, string) (*repository.Container, error) {
	return nil, nil
}
func (f *fakeRepo) BeginExecution(context.Context, string, string) error  { return nil }
func (f *fakeRepo) EndExecution(context.Context, string) error            { return nil }
func (f *fakeRepo) CountByState(context.Context, string) (int, error)     { return 0, nil }
func (f *fakeRepo) CountTotal(context.Context) (int, error)               { return 0, nil }
func (f *fakeRepo) CountExecutionInProgress(context.Context) (int, error) { return 0, nil }
func (f *fakeRepo) FindLongestIdleStopped(context.Context, int) ([]*repository.Container, error) {
	return nil, nil
}
func (f *fakeRepo) FindExpiredRunningIdle(context.Context, time.Time, int) ([]*repository.Container, error) {
	return nil, nil
}
func (f *fakeRepo) FindExpiredStoppedRetention(context.Context, time.Time, int) ([]*repository.Container, error) {
	return nil, nil
}
func (f *fakeRepo) FindLongestIdleRunningNoExec(context.Context, int) ([]*repository.Container, error) {
	return nil, nil
}
func (f *fakeRepo) FindPruneRetryDue(context.Context, time.Time, int) ([]*repository.Container, error) {
	return nil, nil
}
func (f *fakeRepo) ListNonTerminal(context.Context, int) ([]*repository.Container, error) {
	return nil, nil
}
func (f *fakeRepo) BackfillLifecycleDeadlines(context.Context, time.Time, time.Duration, time.Duration, int) (int, error) {
	return 0, nil
}
func (f *fakeRepo) GetIdempotencyRecord(_ context.Context, op, scope, key string) (*repository.IdempotencyRecord, error) {
	record, ok := f.records[f.key(op, scope, key)]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return record, nil
}
func (f *fakeRepo) CreateIdempotencyRecord(_ context.Context, record repository.IdempotencyRecord) (*repository.IdempotencyRecord, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	k := f.key(record.Operation, record.Scope, record.Key)
	if _, exists := f.records[k]; exists {
		return nil, errors.New("duplicate")
	}
	copyRecord := record
	now := time.Now().UTC()
	copyRecord.CreatedAt = now
	copyRecord.UpdatedAt = now
	f.records[k] = &copyRecord
	return &copyRecord, nil
}
func (f *fakeRepo) UpdateIdempotencyRecordResult(_ context.Context, op, scope, key string, requestHash string, statusCode int, responseBody string) (*repository.IdempotencyRecord, error) {
	k := f.key(op, scope, key)
	record, ok := f.records[k]
	if !ok {
		return nil, repository.ErrNotFound
	}
	if record.RequestHash != requestHash {
		return nil, repository.ErrStateConflict
	}
	record.StatusCode = statusCode
	record.ResponseBody = &responseBody
	record.UpdatedAt = time.Now().UTC()
	return record, nil
}
func (f *fakeRepo) DeleteExpiredIdempotencyRecords(context.Context, time.Time) (int, error) {
	return 0, nil
}

func TestBeginAndReplay(t *testing.T) {
	repo := &fakeRepo{records: map[string]*repository.IdempotencyRecord{}}
	svc := NewService(repo, time.Hour)

	begin, err := svc.Begin(context.Background(), "containers.create", "containers:create", "key1", testHash)
	if err != nil {
		t.Fatalf("begin should succeed: %v", err)
	}
	if begin.Replay {
		t.Fatal("expected first begin not to replay")
	}

	if err := svc.Complete(context.Background(), "containers.create", "containers:create", "key1", testHash, 201, `{"id":"c1"}`); err != nil {
		t.Fatalf("complete should succeed: %v", err)
	}

	replay, err := svc.Begin(context.Background(), "containers.create", "containers:create", "key1", testHash)
	if err != nil {
		t.Fatalf("replay begin should succeed: %v", err)
	}
	if !replay.Replay || replay.StatusCode != 201 {
		t.Fatalf("expected replay with status 201, got replay=%v status=%d", replay.Replay, replay.StatusCode)
	}
}

func TestBeginRejectsHashMismatch(t *testing.T) {
	repo := &fakeRepo{records: map[string]*repository.IdempotencyRecord{}}
	svc := NewService(repo, time.Hour)

	_, _ = svc.Begin(context.Background(), "containers.create", "containers:create", "key1", testHash)
	_, err := svc.Begin(context.Background(), "containers.create", "containers:create", "key1", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if err == nil || !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("expected hash mismatch, got %v", err)
	}
}

func TestCompleteTruncatesStoredResponseBody(t *testing.T) {
	repo := &fakeRepo{records: map[string]*repository.IdempotencyRecord{}}
	svc := NewService(repo, time.Hour)

	_, err := svc.Begin(context.Background(), "containers.create", "containers:create", "key1", testHash)
	if err != nil {
		t.Fatalf("begin should succeed: %v", err)
	}

	longBody := strings.Repeat("a", maxStoredResponseBytes+100)
	if err := svc.Complete(context.Background(), "containers.create", "containers:create", "key1", testHash, 201, longBody); err != nil {
		t.Fatalf("complete should succeed: %v", err)
	}

	record, err := repo.GetIdempotencyRecord(context.Background(), "containers.create", "containers:create", "key1")
	if err != nil {
		t.Fatalf("load record should succeed: %v", err)
	}
	if record.ResponseBody == nil {
		t.Fatal("expected persisted response body")
	}
	if len(*record.ResponseBody) > maxStoredResponseBytes {
		t.Fatalf("expected truncated body <= %d bytes, got %d", maxStoredResponseBytes, len(*record.ResponseBody))
	}
	if !strings.HasSuffix(*record.ResponseBody, truncatedMarker) {
		t.Fatalf("expected truncated marker suffix")
	}
}

func TestBeginRejectsInvalidOperationOrScope(t *testing.T) {
	repo := &fakeRepo{records: map[string]*repository.IdempotencyRecord{}}
	svc := NewService(repo, time.Hour)

	_, err := svc.Begin(context.Background(), "unknown.operation", "containers:create", "key1", testHash)
	if err == nil || !errors.Is(err, ErrInvalidOperation) {
		t.Fatalf("expected invalid operation error, got %v", err)
	}

	_, err = svc.Begin(context.Background(), "containers.create", "wrong-scope", "key1", testHash)
	if err == nil || !errors.Is(err, ErrInvalidScope) {
		t.Fatalf("expected invalid scope error, got %v", err)
	}
}

func TestCompleteRedactsNonSuccessReplayBody(t *testing.T) {
	repo := &fakeRepo{records: map[string]*repository.IdempotencyRecord{}}
	svc := NewService(repo, time.Hour)

	_, err := svc.Begin(context.Background(), "containers.create", "containers:create", "key1", testHash)
	if err != nil {
		t.Fatalf("begin should succeed: %v", err)
	}
	if err := svc.Complete(context.Background(), "containers.create", "containers:create", "key1", testHash, 500, `{"error":"secret-ish details"}`); err != nil {
		t.Fatalf("complete should succeed: %v", err)
	}

	record, err := repo.GetIdempotencyRecord(context.Background(), "containers.create", "containers:create", "key1")
	if err != nil {
		t.Fatalf("load record should succeed: %v", err)
	}
	if record.ResponseBody == nil || *record.ResponseBody != "{}" {
		t.Fatalf("expected redacted fallback body, got %v", record.ResponseBody)
	}
}

func TestBeginAllowsRetryWhenPriorStatusNonReplayable(t *testing.T) {
	repo := &fakeRepo{
		records: map[string]*repository.IdempotencyRecord{
			"containers.create|containers:create|key1": {
				Operation:   "containers.create",
				Scope:       "containers:create",
				Key:         "key1",
				RequestHash: testHash,
				StatusCode:  500,
			},
		},
	}
	svc := NewService(repo, time.Hour)

	begin, err := svc.Begin(context.Background(), "containers.create", "containers:create", "key1", testHash)
	if err != nil {
		t.Fatalf("expected retryable begin, got %v", err)
	}
	if begin.Replay {
		t.Fatal("did not expect replay for non-replayable status")
	}
}

func TestBeginCreateErrorNonUniqueReturnsInfrastructureError(t *testing.T) {
	repo := &fakeRepo{
		records:   map[string]*repository.IdempotencyRecord{},
		createErr: errors.New("db temporarily unavailable"),
	}
	svc := NewService(repo, time.Hour)

	_, err := svc.Begin(context.Background(), "containers.create", "containers:create", "key1", testHash)
	if err == nil || !strings.Contains(err.Error(), "infrastructure failure") {
		t.Fatalf("expected infrastructure failure error, got %v", err)
	}
}

func TestBeginCreateErrorUniqueViolationAllowsReplayPath(t *testing.T) {
	repo := &fakeRepo{
		records: map[string]*repository.IdempotencyRecord{
			"containers.create|containers:create|key1": {
				Operation:    "containers.create",
				Scope:        "containers:create",
				Key:          "key1",
				RequestHash:  testHash,
				StatusCode:   201,
				ResponseBody: stringPtr(`{"id":"c1"}`),
			},
		},
		createErr: &pq.Error{Code: "23505"},
	}
	svc := NewService(repo, time.Hour)

	begin, err := svc.Begin(context.Background(), "containers.create", "containers:create", "key1", testHash)
	if err != nil {
		t.Fatalf("expected replay branch on unique violation, got %v", err)
	}
	if !begin.Replay {
		t.Fatal("expected replay on unique violation with completed prior record")
	}
}

func stringPtr(v string) *string { return &v }
