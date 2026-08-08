package schema

import (
	"github.com/google/uuid"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// IdempotencyRecord stores operation replay outcomes for safe retries.
type IdempotencyRecord struct {
	ent.Schema
}

func (IdempotencyRecord) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).
			Default(uuid.New).
			Immutable(),
		field.String("operation").
			NotEmpty().
			Immutable(),
		field.String("scope").
			NotEmpty().
			Immutable(),
		field.String("key").
			NotEmpty().
			Immutable(),
		field.String("request_hash").
			NotEmpty().
			Immutable(),
		field.Int("status_code").
			Default(0),
		field.String("response_body").
			Comment("Truncated replay body (max 8KiB), persisted only for allowlisted operations and 2xx responses.").
			Optional().
			Nillable(),
		field.Time("expires_at"),
	}
}

func (IdempotencyRecord) Mixin() []ent.Mixin {
	return []ent.Mixin{
		TimeMixin{},
	}
}

func (IdempotencyRecord) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("operation", "scope", "key").Unique(),
		index.Fields("expires_at"),
	}
}
