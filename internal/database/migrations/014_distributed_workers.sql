CREATE TABLE execution_nodes (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL UNIQUE,
    roles jsonb NOT NULL DEFAULT '[]'::jsonb,
    enabled boolean NOT NULL DEFAULT true,
    max_concurrent_jobs integer NOT NULL DEFAULT 6 CHECK (max_concurrent_jobs > 0),
    credential_hash char(64),
    credential_version bigint NOT NULL DEFAULT 0,
    protocol_version integer NOT NULL DEFAULT 1,
    worker_version text,
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending','online','offline','disabled','incompatible','error')),
    heartbeat_at timestamptz,
    last_error text,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE execution_node_enrollments (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id uuid NOT NULL REFERENCES execution_nodes(id) ON DELETE CASCADE,
    token_hash char(64) NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX execution_node_enrollments_active
    ON execution_node_enrollments(expires_at)
    WHERE consumed_at IS NULL;

ALTER TABLE work_items
    ADD COLUMN execution_node_id uuid REFERENCES execution_nodes(id) ON DELETE RESTRICT;
ALTER TABLE codex_thread_controls
    ADD COLUMN execution_node_id uuid REFERENCES execution_nodes(id) ON DELETE RESTRICT;
ALTER TABLE codex_turn_runs
    ADD COLUMN execution_node_id uuid REFERENCES execution_nodes(id) ON DELETE RESTRICT,
    ADD COLUMN worker_event_sequence bigint NOT NULL DEFAULT 0,
    ADD COLUMN worker_terminal_key text;
ALTER TABLE discord_development_environments
    ADD COLUMN execution_node_id uuid REFERENCES execution_nodes(id) ON DELETE RESTRICT;
ALTER TABLE repo_caches
    ADD COLUMN execution_node_id uuid REFERENCES execution_nodes(id) ON DELETE RESTRICT;
ALTER TABLE worktrees
    ADD COLUMN execution_node_id uuid REFERENCES execution_nodes(id) ON DELETE RESTRICT;
ALTER TABLE worker_nodes
    ADD COLUMN execution_node_id uuid REFERENCES execution_nodes(id) ON DELETE SET NULL;

ALTER TABLE repo_caches DROP CONSTRAINT IF EXISTS repo_caches_repository_id_key;
ALTER TABLE repo_caches DROP CONSTRAINT IF EXISTS repo_caches_path_key;
CREATE UNIQUE INDEX repo_caches_node_repository
    ON repo_caches(execution_node_id, repository_id)
    WHERE execution_node_id IS NOT NULL;
CREATE UNIQUE INDEX repo_caches_unplaced_repository
    ON repo_caches(repository_id)
    WHERE execution_node_id IS NULL;
CREATE UNIQUE INDEX repo_caches_node_path
    ON repo_caches(execution_node_id, path)
    WHERE execution_node_id IS NOT NULL;

ALTER TABLE worktrees DROP CONSTRAINT IF EXISTS worktrees_path_key;
CREATE UNIQUE INDEX worktrees_node_path
    ON worktrees(execution_node_id, path)
    WHERE execution_node_id IS NOT NULL;

ALTER TABLE codex_turn_intents
    DROP CONSTRAINT IF EXISTS codex_turn_intents_status_check;
ALTER TABLE codex_turn_intents
    ADD CONSTRAINT codex_turn_intents_status_check CHECK (status IN (
        'placement_pending','queued','dispatching','awaiting_confirmation','running',
        'reconciling','retry_wait','completed','failed','canceled'
    ));

ALTER TABLE discord_attachments
    ADD COLUMN storage_key text,
    ADD COLUMN stored_at timestamptz;
CREATE UNIQUE INDEX discord_attachments_storage_key
    ON discord_attachments(storage_key) WHERE storage_key IS NOT NULL;

CREATE UNIQUE INDEX agent_events_run_external_event
    ON agent_events(run_id, external_event_id)
    WHERE run_id IS NOT NULL AND external_event_id IS NOT NULL;
CREATE UNIQUE INDEX codex_turn_runs_worker_terminal_key
    ON codex_turn_runs(id, worker_terminal_key) WHERE worker_terminal_key IS NOT NULL;

CREATE INDEX codex_controls_execution_node
    ON codex_thread_controls(execution_node_id, status, next_wakeup_at);
CREATE INDEX development_environments_execution_node
    ON discord_development_environments(execution_node_id, status);
