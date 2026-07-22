-- Codex 运行历史不迁移；环境、Forum、Conversation 与 Workspace 配置继续保留。
DELETE FROM codex_thread_controls;

ALTER TABLE execution_nodes ALTER COLUMN protocol_version SET DEFAULT 2;
UPDATE execution_nodes SET protocol_version = 2, status = 'pending', last_error = NULL;

ALTER TABLE discord_development_environments
    DROP COLUMN idle_at,
    ADD COLUMN ssh_public_key text,
    ADD COLUMN ssh_fingerprint text,
    ADD COLUMN ssh_port integer CHECK (ssh_port BETWEEN 1 AND 65535),
    ADD COLUMN ssh_config_revision bigint NOT NULL DEFAULT 0 CHECK (ssh_config_revision >= 0),
    ADD COLUMN ssh_applied_revision bigint NOT NULL DEFAULT 0 CHECK (ssh_applied_revision >= 0),
    ADD COLUMN daemon_status text NOT NULL DEFAULT 'pending'
        CHECK (daemon_status IN ('pending','starting','running','error')),
    ADD COLUMN daemon_error text,
    ADD COLUMN app_server_status text NOT NULL DEFAULT 'pending'
        CHECK (app_server_status IN ('pending','starting','running','error')),
    ADD COLUMN ssh_daemon_status text NOT NULL DEFAULT 'disabled'
        CHECK (ssh_daemon_status IN ('disabled','pending','starting','running','error')),
    ADD COLUMN relay_status text NOT NULL DEFAULT 'pending'
        CHECK (relay_status IN ('pending','starting','running','error')),
    ADD CONSTRAINT discord_development_environments_ssh_pair CHECK (
        (ssh_public_key IS NULL AND ssh_fingerprint IS NULL AND ssh_port IS NULL)
        OR
        (ssh_public_key IS NOT NULL AND ssh_fingerprint IS NOT NULL AND ssh_port IS NOT NULL)
    );

UPDATE discord_development_environments SET ssh_daemon_status = 'pending'
WHERE ssh_public_key IS NOT NULL;

ALTER TABLE discord_development_environments
    DROP CONSTRAINT discord_development_environments_status_check;
ALTER TABLE discord_development_environments
    ADD CONSTRAINT discord_development_environments_status_check CHECK (
        status IN ('pending','building','ready','running','error','deleting')
    );

CREATE UNIQUE INDEX discord_development_environments_node_ssh_port
    ON discord_development_environments(execution_node_id, ssh_port)
    WHERE execution_node_id IS NOT NULL AND ssh_port IS NOT NULL;

ALTER TABLE discord_development_operations
    DROP CONSTRAINT discord_development_operations_operation_check;
DELETE FROM discord_development_operations WHERE operation IN ('start','stop');
ALTER TABLE discord_development_operations
    ADD CONSTRAINT discord_development_operations_operation_check CHECK (
        operation IN ('provision','clone','rebuild','reconfigure','delete_forum','delete_environment')
    );

ALTER TABLE codex_thread_controls
    ADD COLUMN development_environment_id uuid
        REFERENCES discord_development_environments(id) ON DELETE CASCADE;
CREATE INDEX codex_thread_controls_environment
    ON codex_thread_controls(development_environment_id, status);

ALTER TABLE codex_turn_intents
    ADD COLUMN input_surface text CHECK (input_surface IN ('discord','desktop'));
ALTER TABLE codex_turn_intents
    DROP CONSTRAINT codex_turn_intents_status_check;
ALTER TABLE codex_turn_intents
    ADD CONSTRAINT codex_turn_intents_status_check CHECK (status IN (
        'placement_pending','queued','dispatching','awaiting_confirmation','running',
        'waiting_for_user','reconciling','retry_wait','completed','failed','canceled'
    ));

ALTER TABLE codex_turn_runs
    DROP CONSTRAINT codex_turn_runs_status_check;
ALTER TABLE codex_turn_runs
    ADD CONSTRAINT codex_turn_runs_status_check CHECK (status IN (
        'starting','running','waiting_for_user','reconciling','completed','failed','canceled'
    ));

CREATE TABLE desktop_thread_requests (
    id uuid PRIMARY KEY,
    environment_id uuid NOT NULL REFERENCES discord_development_environments(id) ON DELETE CASCADE,
    operation text NOT NULL CHECK (operation IN ('start','fork')),
    request_key char(64) NOT NULL,
    source_control_id uuid REFERENCES codex_thread_controls(id) ON DELETE SET NULL,
    cwd text NOT NULL,
    request_params jsonb NOT NULL,
    status text NOT NULL DEFAULT 'preparing'
        CHECK (status IN ('preparing','post_pending','codex_pending','completed','failed')),
    forum_id uuid REFERENCES discord_forums(id) ON DELETE SET NULL,
    conversation_id uuid REFERENCES discord_conversations(id) ON DELETE SET NULL,
    control_id uuid REFERENCES codex_thread_controls(id) ON DELETE SET NULL,
    external_thread_id text,
    response jsonb,
    error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX desktop_thread_requests_pending
    ON desktop_thread_requests(environment_id, created_at)
    WHERE status IN ('preparing','post_pending','codex_pending');
CREATE UNIQUE INDEX desktop_thread_requests_pending_key
    ON desktop_thread_requests(environment_id, request_key)
    WHERE status IN ('preparing','post_pending','codex_pending');

CREATE TABLE codex_interactive_requests (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    control_id uuid NOT NULL REFERENCES codex_thread_controls(id) ON DELETE CASCADE,
    run_id uuid NOT NULL REFERENCES codex_turn_runs(id) ON DELETE CASCADE,
    thread_id text NOT NULL,
    turn_id text NOT NULL,
    item_id text NOT NULL,
    app_server_generation bigint NOT NULL CHECK (app_server_generation > 0),
    app_server_request_id jsonb NOT NULL,
    questions jsonb NOT NULL,
    draft_answers jsonb NOT NULL DEFAULT '{}'::jsonb,
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending','resolved','expired','interrupted')),
    answer jsonb,
    answer_secret_id uuid REFERENCES encrypted_secrets(id) ON DELETE SET NULL,
    answer_surface text CHECK (answer_surface IN ('desktop','discord','auto')),
    discord_message_id text,
    deadline_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    resolved_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(thread_id, turn_id, item_id)
);
CREATE INDEX codex_interactive_requests_pending
    ON codex_interactive_requests(deadline_at, created_at) WHERE status = 'pending';
