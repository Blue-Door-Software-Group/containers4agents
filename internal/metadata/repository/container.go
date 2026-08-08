package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"entgo.io/ent/dialect/sql"

	"github.com/Blue-Door-Software-Group/containers4agents/ent"
	"github.com/Blue-Door-Software-Group/containers4agents/ent/containermetadata"
	"github.com/Blue-Door-Software-Group/containers4agents/ent/idempotencyrecord"
)

var ErrNotFound = errors.New("container metadata not found")
var ErrStateConflict = errors.New("container metadata state conflict")
var ErrExecutionInProgress = errors.New("container execution already in progress")

type Container struct {
	ID                       string
	ExternalID               string
	State                    string
	ExecInProgress           bool
	Profile                  string
	ImageVersion             string
	ProviderRef              *string
	CreatedAt                time.Time
	UpdatedAt                time.Time
	LastActivityAt           *time.Time
	IdleStopDeadlineAt       *time.Time
	RetentionPruneDeadlineAt *time.Time
	StoppedAt                *time.Time
	PruneRetryCount          int
	NextPruneRetryAt         *time.Time
	LastPruneError           *string
}

type CreateParams struct {
	ExternalID   string
	Profile      string
	ImageVersion string
}

type UpdateStateParams struct {
	ExternalID                  string
	State                       string
	ProviderRef                 *string
	SetActivityNow              bool
	IdleStopDeadlineAt          *time.Time
	ClearIdleStopDeadline       bool
	RetentionPruneDeadlineAt    *time.Time
	ClearRetentionPruneDeadline bool
	StoppedAt                   *time.Time
	ClearStoppedAt              bool
	PruneRetryCount             *int
	NextPruneRetryAt            *time.Time
	ClearNextPruneRetryAt       bool
	LastPruneError              *string
	ClearLastPruneError         bool
}

type IdempotencyRecord struct {
	Operation    string
	Scope        string
	Key          string
	RequestHash  string
	StatusCode   int
	ResponseBody *string
	ExpiresAt    time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type ContainerRepository interface {
	Create(ctx context.Context, params CreateParams) (*Container, error)
	GetByExternalID(ctx context.Context, externalID string) (*Container, error)
	UpdateState(ctx context.Context, params UpdateStateParams) (*Container, error)
	UpdateStateIfCurrent(ctx context.Context, params UpdateStateParams, expectedCurrentState string) (*Container, error)
	BeginExecution(ctx context.Context, externalID string, expectedState string) error
	EndExecution(ctx context.Context, externalID string) error
	CountByState(ctx context.Context, state string) (int, error)
	CountTotal(ctx context.Context) (int, error)
	CountExecutionInProgress(ctx context.Context) (int, error)
	FindLongestIdleStopped(ctx context.Context, limit int) ([]*Container, error)
	FindExpiredRunningIdle(ctx context.Context, now time.Time, limit int) ([]*Container, error)
	FindExpiredStoppedRetention(ctx context.Context, now time.Time, limit int) ([]*Container, error)
	FindLongestIdleRunningNoExec(ctx context.Context, limit int) ([]*Container, error)
	FindPruneRetryDue(ctx context.Context, now time.Time, limit int) ([]*Container, error)
	ListNonTerminal(ctx context.Context, limit int) ([]*Container, error)
	BackfillLifecycleDeadlines(ctx context.Context, now time.Time, idleTimeout time.Duration, retentionTimeout time.Duration, limit int) (int, error)
	GetIdempotencyRecord(ctx context.Context, operation, scope, key string) (*IdempotencyRecord, error)
	CreateIdempotencyRecord(ctx context.Context, record IdempotencyRecord) (*IdempotencyRecord, error)
	UpdateIdempotencyRecordResult(ctx context.Context, operation, scope, key string, requestHash string, statusCode int, responseBody string) (*IdempotencyRecord, error)
	DeleteExpiredIdempotencyRecords(ctx context.Context, now time.Time) (int, error)
}

type EntContainerRepository struct {
	client *ent.Client
}

func NewEntContainerRepository(client *ent.Client) *EntContainerRepository {
	return &EntContainerRepository{client: client}
}

func (r *EntContainerRepository) Create(ctx context.Context, params CreateParams) (*Container, error) {
	row, err := r.client.ContainerMetadata.
		Create().
		SetExternalID(params.ExternalID).
		SetProfile(params.Profile).
		SetImageVersion(params.ImageVersion).
		SetState("creating").
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create container metadata: %w", err)
	}
	return mapRow(row), nil
}

func (r *EntContainerRepository) GetByExternalID(ctx context.Context, externalID string) (*Container, error) {
	row, err := r.client.ContainerMetadata.
		Query().
		Where(containermetadata.ExternalIDEQ(externalID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("query container metadata by external_id: %w", err)
	}
	return mapRow(row), nil
}

func (r *EntContainerRepository) UpdateState(ctx context.Context, params UpdateStateParams) (*Container, error) {
	update := r.client.ContainerMetadata.
		Update().
		Where(containermetadata.ExternalIDEQ(params.ExternalID)).
		SetState(params.State)

	if params.ProviderRef != nil {
		update.SetProviderRef(*params.ProviderRef)
	}
	if params.SetActivityNow {
		update.SetLastActivityAt(time.Now())
	}
	if params.IdleStopDeadlineAt != nil {
		update.SetIdleStopDeadlineAt(*params.IdleStopDeadlineAt)
	}
	if params.ClearIdleStopDeadline {
		update.ClearIdleStopDeadlineAt()
	}
	if params.RetentionPruneDeadlineAt != nil {
		update.SetRetentionPruneDeadlineAt(*params.RetentionPruneDeadlineAt)
	}
	if params.ClearRetentionPruneDeadline {
		update.ClearRetentionPruneDeadlineAt()
	}
	if params.StoppedAt != nil {
		update.SetStoppedAt(*params.StoppedAt)
	}
	if params.ClearStoppedAt {
		update.ClearStoppedAt()
	}
	if params.PruneRetryCount != nil {
		update.SetPruneRetryCount(*params.PruneRetryCount)
	}
	if params.NextPruneRetryAt != nil {
		update.SetNextPruneRetryAt(*params.NextPruneRetryAt)
	}
	if params.ClearNextPruneRetryAt {
		update.ClearNextPruneRetryAt()
	}
	if params.LastPruneError != nil {
		update.SetLastPruneError(*params.LastPruneError)
	}
	if params.ClearLastPruneError {
		update.ClearLastPruneError()
	}

	count, err := update.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update container metadata state: %w", err)
	}
	if count == 0 {
		return nil, ErrNotFound
	}

	return r.GetByExternalID(ctx, params.ExternalID)
}

func (r *EntContainerRepository) UpdateStateIfCurrent(
	ctx context.Context,
	params UpdateStateParams,
	expectedCurrentState string,
) (*Container, error) {
	update := r.client.ContainerMetadata.
		Update().
		Where(
			containermetadata.ExternalIDEQ(params.ExternalID),
			containermetadata.StateEQ(expectedCurrentState),
		).
		SetState(params.State)

	if params.ProviderRef != nil {
		update.SetProviderRef(*params.ProviderRef)
	}
	if params.SetActivityNow {
		update.SetLastActivityAt(time.Now())
	}
	if params.IdleStopDeadlineAt != nil {
		update.SetIdleStopDeadlineAt(*params.IdleStopDeadlineAt)
	}
	if params.ClearIdleStopDeadline {
		update.ClearIdleStopDeadlineAt()
	}
	if params.RetentionPruneDeadlineAt != nil {
		update.SetRetentionPruneDeadlineAt(*params.RetentionPruneDeadlineAt)
	}
	if params.ClearRetentionPruneDeadline {
		update.ClearRetentionPruneDeadlineAt()
	}
	if params.StoppedAt != nil {
		update.SetStoppedAt(*params.StoppedAt)
	}
	if params.ClearStoppedAt {
		update.ClearStoppedAt()
	}
	if params.PruneRetryCount != nil {
		update.SetPruneRetryCount(*params.PruneRetryCount)
	}
	if params.NextPruneRetryAt != nil {
		update.SetNextPruneRetryAt(*params.NextPruneRetryAt)
	}
	if params.ClearNextPruneRetryAt {
		update.ClearNextPruneRetryAt()
	}
	if params.LastPruneError != nil {
		update.SetLastPruneError(*params.LastPruneError)
	}
	if params.ClearLastPruneError {
		update.ClearLastPruneError()
	}

	count, err := update.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("conditional update container metadata state: %w", err)
	}
	if count == 0 {
		row, getErr := r.GetByExternalID(ctx, params.ExternalID)
		if getErr != nil {
			if errors.Is(getErr, ErrNotFound) {
				return nil, ErrNotFound
			}
			return nil, fmt.Errorf("load row after conditional update failure: %w", getErr)
		}
		if row.State != expectedCurrentState {
			return nil, ErrStateConflict
		}
		return nil, ErrNotFound
	}

	return r.GetByExternalID(ctx, params.ExternalID)
}

func (r *EntContainerRepository) FindLongestIdleStopped(ctx context.Context, limit int) ([]*Container, error) {
	if limit <= 0 {
		limit = 1
	}

	rows, err := r.client.ContainerMetadata.
		Query().
		Where(
			containermetadata.StateEQ("stopped"),
			containermetadata.LastActivityAtNotNil(),
		).
		Order(
			containermetadata.ByLastActivityAt(sql.OrderAsc()),
			containermetadata.ByCreatedAt(sql.OrderAsc()),
		).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query longest idle stopped containers: %w", err)
	}

	result := make([]*Container, 0, len(rows))
	for _, row := range rows {
		result = append(result, mapRow(row))
	}
	return result, nil
}

func (r *EntContainerRepository) FindExpiredRunningIdle(ctx context.Context, now time.Time, limit int) ([]*Container, error) {
	if limit <= 0 {
		limit = 1
	}
	rows, err := r.client.ContainerMetadata.
		Query().
		Where(
			containermetadata.StateEQ("running"),
			containermetadata.ExecInProgressEQ(false),
			containermetadata.IdleStopDeadlineAtLTE(now),
		).
		Order(
			containermetadata.ByIdleStopDeadlineAt(sql.OrderAsc()),
			containermetadata.ByCreatedAt(sql.OrderAsc()),
		).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query expired running idle containers: %w", err)
	}
	return mapRows(rows), nil
}

func (r *EntContainerRepository) FindExpiredStoppedRetention(ctx context.Context, now time.Time, limit int) ([]*Container, error) {
	if limit <= 0 {
		limit = 1
	}
	rows, err := r.client.ContainerMetadata.
		Query().
		Where(
			containermetadata.StateEQ("stopped"),
			containermetadata.RetentionPruneDeadlineAtLTE(now),
		).
		Order(
			containermetadata.ByRetentionPruneDeadlineAt(sql.OrderAsc()),
			containermetadata.ByCreatedAt(sql.OrderAsc()),
		).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query expired stopped retention containers: %w", err)
	}
	return mapRows(rows), nil
}

func (r *EntContainerRepository) FindLongestIdleRunningNoExec(ctx context.Context, limit int) ([]*Container, error) {
	if limit <= 0 {
		limit = 1
	}
	rows, err := r.client.ContainerMetadata.
		Query().
		Where(
			containermetadata.StateEQ("running"),
			containermetadata.ExecInProgressEQ(false),
			containermetadata.LastActivityAtNotNil(),
		).
		Order(
			containermetadata.ByLastActivityAt(sql.OrderAsc()),
			containermetadata.ByCreatedAt(sql.OrderAsc()),
		).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query longest idle running no-exec containers: %w", err)
	}
	return mapRows(rows), nil
}

func (r *EntContainerRepository) FindPruneRetryDue(ctx context.Context, now time.Time, limit int) ([]*Container, error) {
	if limit <= 0 {
		limit = 1
	}
	rows, err := r.client.ContainerMetadata.
		Query().
		Where(
			containermetadata.StateEQ("failed"),
			containermetadata.NextPruneRetryAtLTE(now),
			containermetadata.ProviderRefNotNil(),
		).
		Order(
			containermetadata.ByNextPruneRetryAt(sql.OrderAsc()),
			containermetadata.ByCreatedAt(sql.OrderAsc()),
		).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query prune retry due containers: %w", err)
	}
	return mapRows(rows), nil
}

func (r *EntContainerRepository) ListNonTerminal(ctx context.Context, limit int) ([]*Container, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.client.ContainerMetadata.
		Query().
		Where(
			containermetadata.StateIn("creating", "stopping", "starting", "pruning", "failed"),
		).
		Order(containermetadata.ByUpdatedAt(sql.OrderAsc())).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query non-terminal containers: %w", err)
	}
	return mapRows(rows), nil
}

func (r *EntContainerRepository) BackfillLifecycleDeadlines(
	ctx context.Context,
	now time.Time,
	idleTimeout time.Duration,
	retentionTimeout time.Duration,
	limit int,
) (int, error) {
	if limit <= 0 {
		limit = 100
	}
	updated := 0

	runningRows, err := r.client.ContainerMetadata.
		Query().
		Where(
			containermetadata.StateEQ("running"),
			containermetadata.IdleStopDeadlineAtIsNil(),
		).
		Order(containermetadata.ByUpdatedAt(sql.OrderAsc())).
		Limit(limit).
		All(ctx)
	if err != nil {
		return 0, fmt.Errorf("query running rows for deadline backfill: %w", err)
	}
	for _, row := range runningRows {
		base := row.UpdatedAt
		if row.LastActivityAt != nil {
			base = *row.LastActivityAt
		}
		deadline := base.Add(idleTimeout)
		affected, updateErr := r.client.ContainerMetadata.
			Update().
			Where(
				containermetadata.ExternalIDEQ(row.ExternalID),
				containermetadata.StateEQ("running"),
				containermetadata.IdleStopDeadlineAtIsNil(),
			).
			SetIdleStopDeadlineAt(deadline).
			Save(ctx)
		if updateErr != nil {
			return updated, fmt.Errorf("backfill running deadline for %s: %w", row.ExternalID, updateErr)
		}
		updated += affected
	}

	stoppedRows, err := r.client.ContainerMetadata.
		Query().
		Where(
			containermetadata.StateEQ("stopped"),
			containermetadata.RetentionPruneDeadlineAtIsNil(),
		).
		Order(containermetadata.ByUpdatedAt(sql.OrderAsc())).
		Limit(limit).
		All(ctx)
	if err != nil {
		return updated, fmt.Errorf("query stopped rows for deadline backfill: %w", err)
	}
	for _, row := range stoppedRows {
		base := row.UpdatedAt
		if row.LastActivityAt != nil {
			base = *row.LastActivityAt
		}
		if row.StoppedAt != nil {
			base = *row.StoppedAt
		}
		stoppedAt := base
		retentionDeadline := stoppedAt.Add(retentionTimeout)
		update := r.client.ContainerMetadata.
			Update().
			Where(
				containermetadata.ExternalIDEQ(row.ExternalID),
				containermetadata.StateEQ("stopped"),
				containermetadata.RetentionPruneDeadlineAtIsNil(),
			).
			SetRetentionPruneDeadlineAt(retentionDeadline)
		if row.StoppedAt == nil {
			update.SetStoppedAt(stoppedAt)
		}
		affected, updateErr := update.Save(ctx)
		if updateErr != nil {
			return updated, fmt.Errorf("backfill stopped deadline for %s: %w", row.ExternalID, updateErr)
		}
		updated += affected
	}

	return updated, nil
}

func (r *EntContainerRepository) GetIdempotencyRecord(ctx context.Context, operation, scope, key string) (*IdempotencyRecord, error) {
	row, err := r.client.IdempotencyRecord.Query().
		Where(
			idempotencyrecord.OperationEQ(operation),
			idempotencyrecord.ScopeEQ(scope),
			idempotencyrecord.KeyEQ(key),
		).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("query idempotency record: %w", err)
	}
	return mapIdempotencyRow(row), nil
}

func (r *EntContainerRepository) CreateIdempotencyRecord(ctx context.Context, record IdempotencyRecord) (*IdempotencyRecord, error) {
	row, err := r.client.IdempotencyRecord.Create().
		SetOperation(record.Operation).
		SetScope(record.Scope).
		SetKey(record.Key).
		SetRequestHash(record.RequestHash).
		SetExpiresAt(record.ExpiresAt).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create idempotency record: %w", err)
	}
	return mapIdempotencyRow(row), nil
}

func (r *EntContainerRepository) UpdateIdempotencyRecordResult(
	ctx context.Context,
	operation, scope, key string,
	requestHash string,
	statusCode int,
	responseBody string,
) (*IdempotencyRecord, error) {
	count, err := r.client.IdempotencyRecord.Update().
		Where(
			idempotencyrecord.OperationEQ(operation),
			idempotencyrecord.ScopeEQ(scope),
			idempotencyrecord.KeyEQ(key),
			idempotencyrecord.RequestHashEQ(requestHash),
		).
		SetStatusCode(statusCode).
		SetResponseBody(responseBody).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update idempotency record result: %w", err)
	}
	if count == 0 {
		return nil, ErrStateConflict
	}
	return r.GetIdempotencyRecord(ctx, operation, scope, key)
}

func (r *EntContainerRepository) DeleteExpiredIdempotencyRecords(ctx context.Context, now time.Time) (int, error) {
	count, err := r.client.IdempotencyRecord.Delete().
		Where(idempotencyrecord.ExpiresAtLT(now)).
		Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("delete expired idempotency records: %w", err)
	}
	return count, nil
}

func (r *EntContainerRepository) BeginExecution(ctx context.Context, externalID string, expectedState string) error {
	count, err := r.client.ContainerMetadata.
		Update().
		Where(
			containermetadata.ExternalIDEQ(externalID),
			containermetadata.StateEQ(expectedState),
			containermetadata.ExecInProgressEQ(false),
		).
		SetExecInProgress(true).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("begin execution lease: %w", err)
	}
	if count == 1 {
		return nil
	}

	row, getErr := r.GetByExternalID(ctx, externalID)
	if getErr != nil {
		if errors.Is(getErr, ErrNotFound) {
			return ErrNotFound
		}
		return fmt.Errorf("load row after begin execution lease failure: %w", getErr)
	}
	if row.State != expectedState {
		return ErrStateConflict
	}
	if row.ExecInProgress {
		return ErrExecutionInProgress
	}
	return ErrStateConflict
}

func (r *EntContainerRepository) EndExecution(ctx context.Context, externalID string) error {
	count, err := r.client.ContainerMetadata.
		Update().
		Where(containermetadata.ExternalIDEQ(externalID)).
		SetExecInProgress(false).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("end execution lease: %w", err)
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *EntContainerRepository) CountByState(ctx context.Context, state string) (int, error) {
	count, err := r.client.ContainerMetadata.Query().Where(containermetadata.StateEQ(state)).Count(ctx)
	if err != nil {
		return 0, fmt.Errorf("count containers by state %q: %w", state, err)
	}
	return count, nil
}

func (r *EntContainerRepository) CountTotal(ctx context.Context) (int, error) {
	count, err := r.client.ContainerMetadata.Query().Count(ctx)
	if err != nil {
		return 0, fmt.Errorf("count total containers: %w", err)
	}
	return count, nil
}

func (r *EntContainerRepository) CountExecutionInProgress(ctx context.Context) (int, error) {
	count, err := r.client.ContainerMetadata.Query().Where(containermetadata.ExecInProgressEQ(true)).Count(ctx)
	if err != nil {
		return 0, fmt.Errorf("count in-progress executions: %w", err)
	}
	return count, nil
}

func mapRow(row *ent.ContainerMetadata) *Container {
	return &Container{
		ID:                       row.ID.String(),
		ExternalID:               row.ExternalID,
		State:                    row.State,
		ExecInProgress:           row.ExecInProgress,
		Profile:                  row.Profile,
		ImageVersion:             row.ImageVersion,
		ProviderRef:              row.ProviderRef,
		CreatedAt:                row.CreatedAt,
		UpdatedAt:                row.UpdatedAt,
		LastActivityAt:           row.LastActivityAt,
		IdleStopDeadlineAt:       row.IdleStopDeadlineAt,
		RetentionPruneDeadlineAt: row.RetentionPruneDeadlineAt,
		StoppedAt:                row.StoppedAt,
		PruneRetryCount:          row.PruneRetryCount,
		NextPruneRetryAt:         row.NextPruneRetryAt,
		LastPruneError:           row.LastPruneError,
	}
}

func mapRows(rows []*ent.ContainerMetadata) []*Container {
	result := make([]*Container, 0, len(rows))
	for _, row := range rows {
		result = append(result, mapRow(row))
	}
	return result
}

func mapIdempotencyRow(row *ent.IdempotencyRecord) *IdempotencyRecord {
	return &IdempotencyRecord{
		Operation:    row.Operation,
		Scope:        row.Scope,
		Key:          row.Key,
		RequestHash:  row.RequestHash,
		StatusCode:   row.StatusCode,
		ResponseBody: row.ResponseBody,
		ExpiresAt:    row.ExpiresAt,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}
}
