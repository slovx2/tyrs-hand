package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"github.com/google/uuid"
)

type WebhookDelivery struct{ ent.Schema }

func (WebhookDelivery) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "webhook_deliveries"}}
}
func (WebhookDelivery) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New), field.String("provider"), field.String("delivery_id"),
		field.String("event_name"), field.String("action").Optional().Nillable(), field.Bool("signature_valid"),
		field.JSON("payload", map[string]any{}), field.String("status").Default("received"),
		field.String("error").Optional().Nillable(), field.Time("received_at").Default(time.Now),
		field.Time("processed_at").Optional().Nillable(),
	}
}

type WorkItem struct{ ent.Schema }

func (WorkItem) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "work_items"}}
}
func (WorkItem) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New), field.UUID("repository_id", uuid.UUID{}),
		field.String("kind"), field.Int("external_number"), field.String("title").Default(""),
		field.String("state").Default("open"), field.Bool("agent_owned").Default(false),
		field.String("base_sha").Optional().Nillable(), field.String("head_sha").Optional().Nillable(),
		field.Int64("context_version").Default(1), field.Time("closed_at").Optional().Nillable(),
		field.Time("created_at").Default(time.Now), field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

type AgentThread struct{ ent.Schema }

func (AgentThread) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "agent_threads"}}
}
func (AgentThread) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New), field.UUID("work_item_id", uuid.UUID{}),
		field.UUID("agent_profile_id", uuid.UUID{}), field.String("provider"), field.String("external_thread_id"),
		field.Int64("context_version"), field.String("codex_home_key"), field.String("provider_signature").Default(""),
		field.String("rollout_path").Optional().Nillable(),
		field.String("status").Default("active"), field.String("last_turn_id").Optional().Nillable(),
		field.Time("last_used_at").Default(time.Now), field.Time("expires_at").Optional().Nillable(),
		field.Time("created_at").Default(time.Now),
	}
}

type JobIntent struct{ ent.Schema }

func (JobIntent) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "job_intents"}}
}
func (JobIntent) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New), field.UUID("work_item_id", uuid.UUID{}),
		field.UUID("repository_id", uuid.UUID{}), field.UUID("agent_profile_id", uuid.UUID{}),
		field.String("idempotency_key").Unique(), field.String("status").Default("queued"), field.String("instruction"),
		field.JSON("skills", []string{}).Default([]string{}), field.JSON("allowed_tools", []string{}).Default([]string{}),
		field.JSON("dangerous_actions", []string{}).Default([]string{}),
		field.UUID("trigger_rule_id", uuid.UUID{}).Optional().Nillable(),
		field.JSON("trigger_evidence", map[string]any{}).Default(map[string]any{}),
		field.String("actor_login").Default(""), field.String("actor_permission").Default(""),
		field.Int("priority").Default(100), field.Time("available_at").Default(time.Now),
		field.Int("attempt_count").Default(0), field.Int("max_attempts").Default(3),
		field.String("lease_token").Optional().Nillable().Sensitive(), field.Int64("lease_epoch").Default(0),
		field.Time("lease_expires_at").Optional().Nillable(), field.String("worker_id").Optional().Nillable(),
		field.String("last_error").Optional().Nillable(), field.Time("created_at").Default(time.Now),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

type ToolCall struct{ ent.Schema }

func (ToolCall) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "tool_calls"}}
}
func (ToolCall) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New), field.UUID("job_attempt_id", uuid.UUID{}),
		field.String("thread_id"), field.String("turn_id"), field.String("call_id"), field.String("namespace"),
		field.String("tool"), field.JSON("arguments", map[string]any{}), field.JSON("result", map[string]any{}).Optional(),
		field.String("status").Default("running"), field.String("error").Optional().Nillable(),
		field.Time("started_at").Default(time.Now), field.Time("finished_at").Optional().Nillable(),
	}
}
