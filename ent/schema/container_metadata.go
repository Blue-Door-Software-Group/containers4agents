package schema

import (
	"github.com/google/uuid"

	"entgo.io/ent"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// ContainerMetadata stores normalized control-plane state for one external container identity.
type ContainerMetadata struct {
	ent.Schema
}

func (ContainerMetadata) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.String("external_id").
			NotEmpty().
			Unique().
			Immutable(),
		field.String("state").
			Default("creating"),
		field.String("profile").
			NotEmpty(),
		field.String("image_version").
			NotEmpty(),
		field.String("provider_ref").
			Optional().
			Nillable(),
		field.Time("last_activity_at").
			Optional().
			Nillable(),
		field.Bool("exec_in_progress").
			Default(false),
	}
}

func (ContainerMetadata) Mixin() []ent.Mixin {
	return []ent.Mixin{
		TimeMixin{},
	}
}

func (ContainerMetadata) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("external_id").Unique(),
		index.Fields("state"),
		index.Fields("exec_in_progress"),
		index.Fields("last_activity_at"),
		index.Fields("state", "last_activity_at"),
		index.Fields("state", "exec_in_progress"),
		index.Fields("provider_ref"),
	}
}

func (ContainerMetadata) Annotations() []schema.Annotation {
	return nil
}
