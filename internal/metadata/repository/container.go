package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"entgo.io/ent/dialect/sql"

	"github.com/Blue-Door-Software-Group/containers4agents/ent"
	"github.com/Blue-Door-Software-Group/containers4agents/ent/containermetadata"
)

var ErrNotFound = errors.New("container metadata not found")

type Container struct {
	ID             string
	ExternalID     string
	State          string
	Profile        string
	ImageVersion   string
	ProviderRef    *string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	LastActivityAt *time.Time
}

type CreateParams struct {
	ExternalID   string
	Profile      string
	ImageVersion string
}

type UpdateStateParams struct {
	ExternalID     string
	State          string
	ProviderRef    *string
	SetActivityNow bool
}

type ContainerRepository interface {
	Create(ctx context.Context, params CreateParams) (*Container, error)
	GetByExternalID(ctx context.Context, externalID string) (*Container, error)
	UpdateState(ctx context.Context, params UpdateStateParams) (*Container, error)
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

	count, err := update.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update container metadata state: %w", err)
	}
	if count == 0 {
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
		Where(containermetadata.StateEQ("stopped")).
		Order(containermetadata.ByLastActivityAt(sql.OrderAsc())).
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

func mapRow(row *ent.ContainerMetadata) *Container {
	return &Container{
		ID:             row.ID.String(),
		ExternalID:     row.ExternalID,
		State:          row.State,
		Profile:        row.Profile,
		ImageVersion:   row.ImageVersion,
		ProviderRef:    row.ProviderRef,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
		LastActivityAt: row.LastActivityAt,
	}
}
