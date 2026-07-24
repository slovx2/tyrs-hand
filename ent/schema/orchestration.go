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
		field.String("base_ref").Optional().Nillable(), field.String("head_ref").Optional().Nillable(),
		field.String("head_repository").Optional().Nillable(), field.String("html_url").Optional().Nillable(),
		field.UUID("execution_node_id", uuid.UUID{}).Optional().Nillable(),
		field.Time("closed_at").Optional().Nillable(),
		field.Time("created_at").Default(time.Now), field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

type CodexThreadControl struct{ ent.Schema }

func (CodexThreadControl) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "codex_thread_controls"}}
}
func (CodexThreadControl) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New), field.String("source_type"),
		field.UUID("work_item_id", uuid.UUID{}).Optional().Nillable(),
		field.UUID("discord_conversation_id", uuid.UUID{}).Optional().Nillable(),
		field.UUID("repository_id", uuid.UUID{}).Optional().Nillable(),
		field.UUID("agent_profile_id", uuid.UUID{}),
		field.UUID("execution_node_id", uuid.UUID{}).Optional().Nillable(),
		field.String("external_thread_id").Optional().Nillable(),
		field.String("codex_home_key").Optional().Nillable(), field.String("status").Default("idle"),
		field.Int64("next_sequence_no").Default(1), field.UUID("active_intent_id", uuid.UUID{}).Optional().Nillable(),
		field.String("remote_status").Optional().Nillable(), field.String("active_codex_turn_id").Optional().Nillable(),
		field.String("active_client_id").Optional().Nillable(), field.Int64("lease_epoch").Default(0),
		field.Time("lease_expires_at").Optional().Nillable(), field.Time("heartbeat_at").Optional().Nillable(),
		field.Time("last_reconciled_at").Optional().Nillable(), field.Time("next_wakeup_at").Optional().Nillable(),
		field.String("worker_id").Optional().Nillable(), field.String("lease_token").Optional().Nillable().Sensitive(),
		field.String("last_error_code").Optional().Nillable(),
		field.String("last_error_message").Optional().Nillable(),
		field.Time("created_at").Default(time.Now), field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

type CodexTurnIntent struct{ ent.Schema }

func (CodexTurnIntent) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "codex_turn_intents"}}
}
func (CodexTurnIntent) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New), field.UUID("control_id", uuid.UUID{}),
		field.Int64("sequence_no"), field.String("operation").Default("turn_input"),
		field.String("behavior").Optional().Nillable(), field.String("resolved_action").Optional().Nillable(),
		field.UUID("target_intent_id", uuid.UUID{}).Optional().Nillable(),
		field.String("source_type"), field.UUID("work_item_id", uuid.UUID{}).Optional().Nillable(),
		field.UUID("discord_conversation_id", uuid.UUID{}).Optional().Nillable(),
		field.String("discord_message_id").Optional().Nillable(),
		field.UUID("repository_id", uuid.UUID{}).Optional().Nillable(), field.UUID("agent_profile_id", uuid.UUID{}),
		field.UUID("webhook_delivery_id", uuid.UUID{}).Optional().Nillable(),
		field.String("idempotency_key").Unique(), field.String("status").Default("queued"), field.String("instruction"),
		field.JSON("prepared_input", map[string]any{}).Optional(),
		field.JSON("skills", []string{}).Default([]string{}), field.JSON("allowed_tools", []string{}).Default([]string{}),
		field.JSON("dangerous_actions", []string{}).Default([]string{}),
		field.UUID("trigger_rule_id", uuid.UUID{}).Optional().Nillable(),
		field.JSON("trigger_evidence", map[string]any{}).Default(map[string]any{}),
		field.String("actor_login").Default(""), field.String("actor_permission").Default(""),
		field.Int("priority").Default(100), field.Bool("steerable").Default(true),
		field.Time("available_at").Default(time.Now),
		field.Int("attempt_count").Default(0), field.Int("max_attempts").Default(3),
		field.String("codex_submission_id").Optional().Nillable(), field.String("confirmed_codex_turn_id").Optional().Nillable(),
		field.String("last_error_code").Optional().Nillable(), field.String("last_error_message").Optional().Nillable(),
		field.JSON("result", map[string]any{}).Optional(),
		field.String("result_delivery_status").Default("pending"), field.Int("result_delivery_attempt_count").Default(0),
		field.String("result_delivery_error").Optional().Nillable(),
		field.String("result_delivery_token").Optional().Nillable().Sensitive(),
		field.Time("result_delivery_available_at").Optional().Nillable(),
		field.String("reply_policy").Default("silent"),
		field.String("reply_status").Default("pending"), field.Int("reply_hook_block_count").Default(0),
		field.String("reply_tool_call_id").Optional().Nillable(),
		field.Int64("github_comment_id").Optional().Nillable(), field.String("github_comment_url").Optional().Nillable(),
		field.Time("dispatched_at").Optional().Nillable(), field.Time("confirmed_at").Optional().Nillable(),
		field.Time("finished_at").Optional().Nillable(), field.Time("result_delivered_at").Optional().Nillable(),
		field.Time("created_at").Default(time.Now),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

type CodexTurnRun struct{ ent.Schema }

func (CodexTurnRun) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "codex_turn_runs"}}
}
func (CodexTurnRun) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New), field.UUID("control_id", uuid.UUID{}),
		field.UUID("primary_intent_id", uuid.UUID{}), field.Int("attempt"), field.String("worker_id"),
		field.Int64("lease_epoch"), field.String("capability_hash").Sensitive(),
		field.UUID("execution_node_id", uuid.UUID{}).Optional().Nillable(),
		field.Int64("worker_event_sequence").Default(0),
		field.String("worker_terminal_key").Optional().Nillable(),
		field.Int("active_slot").Optional().Nillable(), field.String("status").Default("starting"),
		field.String("codex_submission_id").Optional().Nillable(), field.String("confirmed_codex_turn_id").Optional().Nillable(),
		field.Int("append_count").Default(0), field.Int("max_append_count").Default(5),
		field.Time("started_at").Default(time.Now),
		field.Time("heartbeat_at").Default(time.Now), field.Time("finished_at").Optional().Nillable(),
		field.String("error_code").Optional().Nillable(), field.String("error_message").Optional().Nillable(),
	}
}

type ToolCall struct{ ent.Schema }

func (ToolCall) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "tool_calls"}}
}
func (ToolCall) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New), field.UUID("run_id", uuid.UUID{}),
		field.UUID("intent_id", uuid.UUID{}),
		field.String("thread_id"), field.String("turn_id"), field.String("call_id"), field.String("namespace"),
		field.String("tool"), field.JSON("arguments", map[string]any{}), field.JSON("result", map[string]any{}).Optional(),
		field.String("status").Default("running"), field.String("error").Optional().Nillable(),
		field.Time("started_at").Default(time.Now), field.Time("finished_at").Optional().Nillable(),
	}
}
