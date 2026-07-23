ALTER TABLE codex_thread_controls
    ADD COLUMN lifecycle_state text NOT NULL DEFAULT 'active'
        CHECK (lifecycle_state IN ('active','archive_pending','archived','unarchive_pending')),
    ADD COLUMN lifecycle_revision bigint NOT NULL DEFAULT 0
        CHECK (lifecycle_revision >= 0),
    ADD COLUMN lifecycle_last_error text,
    ADD COLUMN app_server_lifecycle_generation bigint NOT NULL DEFAULT 0
        CHECK (app_server_lifecycle_generation >= 0),
    ADD COLUMN app_server_lifecycle_sequence bigint NOT NULL DEFAULT 0
        CHECK (app_server_lifecycle_sequence >= 0);

ALTER TABLE discord_conversations
    ADD COLUMN lifecycle_state text NOT NULL DEFAULT 'active'
        CHECK (lifecycle_state IN ('active','archive_pending','archived','unarchive_pending')),
    ADD COLUMN lifecycle_revision bigint NOT NULL DEFAULT 0
        CHECK (lifecycle_revision >= 0),
    ADD COLUMN discord_lifecycle_applied_revision bigint NOT NULL DEFAULT 0
        CHECK (discord_lifecycle_applied_revision >= 0),
    ADD COLUMN lifecycle_card_message_id text,
    ADD COLUMN lifecycle_projection_error text;

CREATE TABLE codex_thread_lifecycle_requests (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    control_id uuid NOT NULL REFERENCES codex_thread_controls(id) ON DELETE CASCADE,
    environment_id uuid NOT NULL
        REFERENCES discord_development_environments(id) ON DELETE CASCADE,
    source text NOT NULL CHECK (source IN ('desktop','discord')),
    desired_state text NOT NULL CHECK (desired_state IN ('active','archived')),
    status text NOT NULL CHECK (status IN (
        'waiting_for_turn','applying','completed','failed','canceled'
    )),
    revision bigint NOT NULL CHECK (revision > 0),
    response jsonb,
    error text,
    requested_by_discord_user_id text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    UNIQUE(control_id, revision)
);
CREATE INDEX codex_thread_lifecycle_requests_pending
    ON codex_thread_lifecycle_requests(environment_id, created_at)
    WHERE status IN ('waiting_for_turn','applying');

ALTER TABLE execution_nodes ALTER COLUMN protocol_version SET DEFAULT 5;
UPDATE execution_nodes SET protocol_version = 5, status = 'pending', last_error = NULL;
