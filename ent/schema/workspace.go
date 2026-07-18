package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"github.com/google/uuid"
)

type WorkerNode struct{ ent.Schema }

func (WorkerNode) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "worker_nodes"}}
}
func (WorkerNode) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").Unique(), field.String("version"), field.String("status").Default("online"),
		field.JSON("metadata", map[string]any{}).Default(map[string]any{}), field.Time("heartbeat_at").Default(time.Now),
		field.Time("started_at").Default(time.Now),
	}
}

type RepoCache struct{ ent.Schema }

func (RepoCache) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "repo_caches"}}
}
func (RepoCache) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New), field.UUID("repository_id", uuid.UUID{}).Unique(),
		field.String("path").Unique(), field.String("status").Default("ready"), field.Int64("size_bytes").Default(0),
		field.Time("last_fetch_at").Optional().Nillable(), field.Time("last_used_at").Default(time.Now),
		field.String("error").Optional().Nillable(),
	}
}

type Worktree struct{ ent.Schema }

func (Worktree) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "worktrees"}}
}
func (Worktree) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New), field.UUID("work_item_id", uuid.UUID{}).Unique(),
		field.UUID("repo_cache_id", uuid.UUID{}), field.String("path").Unique(), field.String("branch"),
		field.String("base_sha"), field.String("head_sha"), field.String("status").Default("ready"),
		field.Bool("dirty").Default(false), field.Time("last_used_at").Default(time.Now),
		field.Time("expires_at").Optional().Nillable(), field.String("error").Optional().Nillable(),
	}
}

type AuditLog struct{ ent.Schema }

func (AuditLog) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "audit_logs"}}
}
func (AuditLog) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id").Unique(), field.UUID("administrator_id", uuid.UUID{}).Optional().Nillable(),
		field.String("action"), field.String("resource_type"), field.String("resource_id").Optional().Nillable(),
		field.String("request_id").Optional().Nillable(), field.String("ip_address").Optional().Nillable(),
		field.JSON("metadata", map[string]any{}).Default(map[string]any{}), field.Time("created_at").Default(time.Now),
	}
}
