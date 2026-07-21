CREATE TABLE codex_runtime_settings (
    scope_type text NOT NULL CHECK (scope_type IN ('repository', 'discord_forum')),
    scope_id uuid NOT NULL,
    model text,
    reasoning_effort text CHECK (reasoning_effort IS NULL OR reasoning_effort IN ('low','medium','high','xhigh')),
    service_tier text CHECK (service_tier IS NULL OR service_tier IN ('standard','fast')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(scope_type, scope_id)
);

ALTER TABLE discord_conversations
    ADD COLUMN model text,
    ADD COLUMN reasoning_effort text,
    ADD COLUMN service_tier text NOT NULL DEFAULT 'standard'
        CHECK (service_tier IN ('standard','fast')),
    ADD COLUMN configuration_status text NOT NULL DEFAULT 'configured'
        CHECK (configuration_status IN ('awaiting','editing','configured')),
    ADD COLUMN configuration_deadline timestamptz,
    ADD COLUMN configured_by_discord_user_id text,
    ADD COLUMN title_rename_status text NOT NULL DEFAULT 'skipped'
        CHECK (title_rename_status IN ('pending','scheduled','completed','failed','skipped')),
    ADD COLUMN generated_title text,
    ADD COLUMN title_renamed_at timestamptz;

ALTER TABLE codex_thread_controls
    ADD COLUMN model text,
    ADD COLUMN reasoning_effort text,
    ADD COLUMN service_tier text CHECK (service_tier IS NULL OR service_tier IN ('standard','fast')),
    ADD COLUMN runtime_preferences_frozen_at timestamptz;

CREATE INDEX discord_conversations_configuration_due
    ON discord_conversations(configuration_deadline)
    WHERE configuration_status IN ('awaiting','editing');
