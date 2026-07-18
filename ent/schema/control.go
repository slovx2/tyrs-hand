package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"github.com/google/uuid"
)

type Administrator struct{ ent.Schema }

func (Administrator) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "administrators"}}
}
func (Administrator) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New), field.String("username").Unique(),
		field.String("password_hash").Sensitive(), field.Bytes("totp_secret_ciphertext").Sensitive(),
		field.JSON("recovery_codes_hash", []string{}).Default([]string{}),
		field.Time("created_at").Default(time.Now), field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

type PlatformSetting struct{ ent.Schema }

func (PlatformSetting) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "platform_settings"}}
}
func (PlatformSetting) Fields() []ent.Field {
	return []ent.Field{
		field.String("setting_key").Unique(), field.JSON("value", map[string]any{}),
		field.Int64("version").Default(1), field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

type SCMInstallation struct{ ent.Schema }

func (SCMInstallation) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "scm_installations"}}
}
func (SCMInstallation) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New), field.String("provider"), field.Int64("external_id"),
		field.String("account_login"), field.String("account_type"), field.Time("suspended_at").Optional().Nillable(),
		field.JSON("metadata", map[string]any{}).Default(map[string]any{}),
		field.Time("created_at").Default(time.Now), field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

type Repository struct{ ent.Schema }

func (Repository) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "repositories"}}
}
func (Repository) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New), field.UUID("installation_id", uuid.UUID{}),
		field.String("provider"), field.Int64("external_id"), field.String("owner"), field.String("name"),
		field.String("default_branch"), field.String("clone_url"), field.Bool("enabled").Default(true),
		field.JSON("metadata", map[string]any{}).Default(map[string]any{}),
		field.Time("created_at").Default(time.Now), field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

type AgentProfile struct{ ent.Schema }

func (AgentProfile) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "agent_profiles"}}
}
func (AgentProfile) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New), field.String("name").Unique(), field.String("provider").Default("codex"),
		field.String("model").Optional().Nillable(), field.String("reasoning_effort").Optional().Nillable(),
		field.String("service_tier").Optional().Nillable(), field.String("sandbox").Default("workspace-write"),
		field.Bool("network_enabled").Default(true), field.String("approval_policy").Default("never"),
		field.JSON("allowed_tools", []string{}).Default([]string{}), field.JSON("config", map[string]any{}).Default(map[string]any{}),
		field.Int64("context_version").Default(1), field.Time("created_at").Default(time.Now),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

type TriggerRule struct{ ent.Schema }

func (TriggerRule) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "trigger_rules"}}
}
func (TriggerRule) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New), field.UUID("repository_id", uuid.UUID{}),
		field.UUID("agent_profile_id", uuid.UUID{}), field.String("name"), field.String("event_name"),
		field.String("action").Optional().Nillable(), field.Bool("enabled").Default(true), field.Int("priority").Default(100),
		field.String("actor_min_permission").Default("triage"), field.Bool("mention_required").Default(false),
		field.String("instruction_template"), field.JSON("skills", []string{}).Default([]string{}),
		field.JSON("allowed_tools", []string{}).Default([]string{}), field.JSON("dangerous_actions", []string{}).Default([]string{}),
		field.JSON("filters", map[string]any{}).Default(map[string]any{}), field.Int64("version").Default(1),
		field.Time("created_at").Default(time.Now), field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}
