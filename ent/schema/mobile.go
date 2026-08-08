package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

type ClientMaterialization struct{ ent.Schema }

func (ClientMaterialization) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "client_materializations"}}
}

func (ClientMaterialization) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New),
		field.UUID("device_id", uuid.UUID{}).Optional().Nillable(),
		field.UUID("workspace_id", uuid.UUID{}),
		field.UUID("worker_id", uuid.UUID{}),
		field.String("source_type"),
		field.String("source_key"),
		field.String("client_id").Optional().Nillable(),
		field.String("original_filename"),
		field.String("media_type"),
		field.Int64("size_bytes"),
		field.String("sha256").MinLen(64).MaxLen(64),
		field.String("storage_key"),
		field.String("status").Default("queued"),
		field.String("lease_token_hash").Optional().Nillable().Sensitive(),
		field.Time("lease_expires_at").Optional().Nillable(),
		field.String("remote_path").Optional().Nillable(),
		field.String("error").Optional().Nillable(),
		field.Time("created_at").Default(time.Now),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
		field.Time("completed_at").Optional().Nillable(),
	}
}

func (ClientMaterialization) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("source_type", "source_key").Unique(),
		index.Fields("worker_id", "created_at", "id"),
	}
}
