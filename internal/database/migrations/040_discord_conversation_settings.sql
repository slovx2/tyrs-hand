CREATE TABLE discord_user_codex_preferences (
    guild_id text NOT NULL REFERENCES discord_guilds(guild_id) ON DELETE CASCADE,
    discord_user_id text NOT NULL,
    model text,
    reasoning_effort text
        CHECK (reasoning_effort IS NULL OR reasoning_effort IN ('low','medium','high','xhigh')),
    service_tier text NOT NULL DEFAULT 'standard'
        CHECK (service_tier IN ('standard','fast')),
    collaboration_mode text NOT NULL DEFAULT 'default'
        CHECK (collaboration_mode IN ('default','plan')),
    trigger_mode text NOT NULL DEFAULT 'interactive'
        CHECK (trigger_mode IN ('interactive','discussion')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(guild_id, discord_user_id)
);

ALTER TABLE discord_conversations
    ADD COLUMN settings_revision bigint NOT NULL DEFAULT 0
        CHECK (settings_revision >= 0);

ALTER TABLE codex_thread_controls
    ADD COLUMN settings_revision bigint NOT NULL DEFAULT 0
        CHECK (settings_revision >= 0),
    ADD COLUMN applied_model text,
    ADD COLUMN applied_reasoning_effort text,
    ADD COLUMN applied_service_tier text,
    ADD COLUMN applied_collaboration_mode text
        CHECK (applied_collaboration_mode IS NULL
            OR applied_collaboration_mode IN ('default','plan')),
    ADD COLUMN applied_settings_revision bigint
        CHECK (applied_settings_revision IS NULL OR applied_settings_revision >= 0),
    ADD COLUMN settings_applied_at timestamptz;

UPDATE codex_thread_controls control
SET model = conversation.model,
    reasoning_effort = conversation.reasoning_effort,
    service_tier = conversation.service_tier,
    collaboration_mode = conversation.collaboration_mode,
    settings_revision = conversation.settings_revision,
    runtime_preferences_frozen_at = now()
FROM discord_conversations conversation
WHERE control.discord_conversation_id = conversation.id;

ALTER TABLE codex_turn_runs
    ADD COLUMN model text,
    ADD COLUMN reasoning_effort text,
    ADD COLUMN service_tier text,
    ADD COLUMN settings_revision bigint NOT NULL DEFAULT 0
        CHECK (settings_revision >= 0),
    ADD COLUMN applied_model text,
    ADD COLUMN applied_reasoning_effort text,
    ADD COLUMN applied_service_tier text,
    ADD COLUMN applied_collaboration_mode text
        CHECK (applied_collaboration_mode IS NULL
            OR applied_collaboration_mode IN ('default','plan')),
    ADD COLUMN applied_settings_revision bigint
        CHECK (applied_settings_revision IS NULL OR applied_settings_revision >= 0),
    ADD COLUMN settings_applied_at timestamptz;

ALTER TABLE execution_nodes ALTER COLUMN protocol_version SET DEFAULT 15;
UPDATE execution_nodes
SET protocol_version = 15, status = 'pending', last_error = NULL, updated_at = now();
