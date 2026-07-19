-- 当前环境只有测试数据，直接用持久化 Codex Control 替换旧 Job/Thread 队列。
DROP TABLE IF EXISTS tool_calls;
DROP TABLE IF EXISTS agent_events;
DROP TABLE IF EXISTS discord_turn_contributors;
DROP TABLE IF EXISTS job_attempts;
DROP TABLE IF EXISTS job_intents;
DROP TABLE IF EXISTS agent_threads;
DROP TABLE IF EXISTS discord_conversation_memories;
DROP TABLE IF EXISTS work_item_memories;

UPDATE agent_profiles
SET allowed_tools = COALESCE((
    SELECT jsonb_agg(value ORDER BY ordinal)
    FROM jsonb_array_elements(allowed_tools) WITH ORDINALITY AS item(value, ordinal)
    WHERE value <> '"add_issue_comment"'::jsonb
), '[]'::jsonb), updated_at = now();

CREATE TABLE codex_thread_controls (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    source_type text NOT NULL CHECK (source_type IN ('github_work_item', 'discord_conversation')),
    work_item_id uuid REFERENCES work_items(id) ON DELETE CASCADE,
    discord_conversation_id uuid REFERENCES discord_conversations(id) ON DELETE CASCADE,
    repository_id uuid REFERENCES repositories(id) ON DELETE CASCADE,
    agent_profile_id uuid NOT NULL REFERENCES agent_profiles(id),
    context_version bigint NOT NULL,
    external_thread_id text,
    provider text NOT NULL DEFAULT 'codex',
    codex_home_key text,
    provider_signature text,
    thread_generation integer NOT NULL DEFAULT 1 CHECK (thread_generation > 0),
    status text NOT NULL DEFAULT 'idle'
        CHECK (status IN ('idle', 'dispatching', 'active', 'stopping', 'reconciling', 'error')),
    next_sequence_no bigint NOT NULL DEFAULT 1 CHECK (next_sequence_no > 0),
    active_intent_id uuid,
    remote_status text,
    active_codex_turn_id text,
    active_client_id text,
    last_reconciled_at timestamptz,
    worker_id text,
    lease_token char(64),
    lease_epoch bigint NOT NULL DEFAULT 0 CHECK (lease_epoch >= 0),
    lease_expires_at timestamptz,
    heartbeat_at timestamptz,
    next_wakeup_at timestamptz,
    last_error_code text,
    last_error_message text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (
        (source_type = 'github_work_item' AND work_item_id IS NOT NULL
            AND discord_conversation_id IS NULL AND repository_id IS NOT NULL)
        OR
        (source_type = 'discord_conversation' AND work_item_id IS NULL
            AND discord_conversation_id IS NOT NULL)
    )
);
CREATE UNIQUE INDEX codex_controls_github_scope
    ON codex_thread_controls(work_item_id, agent_profile_id, context_version)
    WHERE work_item_id IS NOT NULL;
CREATE UNIQUE INDEX codex_controls_discord_scope
    ON codex_thread_controls(discord_conversation_id, agent_profile_id, context_version)
    WHERE discord_conversation_id IS NOT NULL;
CREATE UNIQUE INDEX codex_controls_external_thread
    ON codex_thread_controls(external_thread_id) WHERE external_thread_id IS NOT NULL;
CREATE INDEX codex_controls_claim
    ON codex_thread_controls(status, lease_expires_at, next_wakeup_at, created_at);

CREATE TABLE codex_turn_intents (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    control_id uuid NOT NULL REFERENCES codex_thread_controls(id) ON DELETE CASCADE,
    sequence_no bigint NOT NULL CHECK (sequence_no > 0),
    operation text NOT NULL DEFAULT 'turn_input' CHECK (operation IN ('turn_input', 'interrupt')),
    behavior text CHECK (behavior IN ('start_when_idle', 'steer_if_active')),
    resolved_action text CHECK (resolved_action IN ('start', 'steer', 'start_after_active', 'interrupt')),
    target_intent_id uuid REFERENCES codex_turn_intents(id) ON DELETE SET NULL,
    source_type text NOT NULL CHECK (source_type IN ('github_work_item', 'discord_conversation')),
    work_item_id uuid REFERENCES work_items(id) ON DELETE CASCADE,
    discord_conversation_id uuid REFERENCES discord_conversations(id) ON DELETE CASCADE,
    discord_message_id text REFERENCES discord_input_messages(message_id) ON DELETE SET NULL,
    repository_id uuid REFERENCES repositories(id) ON DELETE CASCADE,
    agent_profile_id uuid NOT NULL REFERENCES agent_profiles(id),
    webhook_delivery_id uuid REFERENCES webhook_deliveries(id),
    trigger_rule_id uuid REFERENCES trigger_rules(id) ON DELETE SET NULL,
    trigger_evidence jsonb NOT NULL DEFAULT '{}'::jsonb,
    idempotency_key text NOT NULL UNIQUE,
    status text NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'dispatching', 'awaiting_confirmation', 'running',
            'reconciling', 'retry_wait', 'completed', 'failed', 'canceled')),
    instruction text NOT NULL DEFAULT '',
    prepared_input jsonb,
    skills jsonb NOT NULL DEFAULT '[]'::jsonb,
    allowed_tools jsonb NOT NULL DEFAULT '[]'::jsonb,
    dangerous_actions jsonb NOT NULL DEFAULT '[]'::jsonb,
    priority integer NOT NULL DEFAULT 100,
    actor_login text NOT NULL DEFAULT '',
    actor_permission text NOT NULL DEFAULT 'none',
    steerable boolean NOT NULL DEFAULT true,
    codex_submission_id text,
    confirmed_codex_turn_id text,
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    max_attempts integer NOT NULL DEFAULT 3 CHECK (max_attempts > 0),
    available_at timestamptz NOT NULL DEFAULT now(),
    last_error_code text,
    last_error_message text,
    result jsonb,
    result_delivery_status text NOT NULL DEFAULT 'pending'
        CHECK (result_delivery_status IN ('pending', 'delivering', 'retry_wait', 'delivered', 'failed', 'skipped')),
    result_delivery_attempt_count integer NOT NULL DEFAULT 0,
    result_delivery_error text,
    result_delivery_token text,
    result_delivery_available_at timestamptz,
    reply_policy text NOT NULL DEFAULT 'silent' CHECK (reply_policy IN ('required', 'silent')),
    reply_status text NOT NULL DEFAULT 'pending'
        CHECK (reply_status IN ('pending', 'sending', 'delivered', 'failed', 'skipped')),
    reply_hook_block_count integer NOT NULL DEFAULT 0 CHECK (reply_hook_block_count >= 0),
    reply_tool_call_id text,
    github_comment_id bigint,
    github_comment_url text,
    dispatched_at timestamptz,
    confirmed_at timestamptz,
    finished_at timestamptz,
    result_delivered_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(control_id, sequence_no),
    CHECK (
        (source_type = 'github_work_item' AND work_item_id IS NOT NULL
            AND discord_conversation_id IS NULL AND repository_id IS NOT NULL)
        OR
        (source_type = 'discord_conversation' AND work_item_id IS NULL
            AND discord_conversation_id IS NOT NULL)
    )
);
ALTER TABLE codex_thread_controls
    ADD CONSTRAINT codex_controls_active_intent
    FOREIGN KEY(active_intent_id) REFERENCES codex_turn_intents(id) ON DELETE SET NULL;
CREATE INDEX codex_intents_queue
    ON codex_turn_intents(control_id, status, available_at, sequence_no);
CREATE INDEX codex_intents_work_item ON codex_turn_intents(work_item_id, created_at);
CREATE INDEX codex_intents_discord ON codex_turn_intents(discord_conversation_id, created_at);
CREATE INDEX codex_intents_delivery
    ON codex_turn_intents(result_delivery_status, result_delivery_available_at, finished_at);

CREATE TABLE codex_turn_runs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    control_id uuid NOT NULL REFERENCES codex_thread_controls(id) ON DELETE CASCADE,
    primary_intent_id uuid NOT NULL REFERENCES codex_turn_intents(id) ON DELETE CASCADE,
    attempt integer NOT NULL CHECK (attempt > 0),
    worker_id text NOT NULL,
    lease_epoch bigint NOT NULL,
    capability_hash char(64) NOT NULL UNIQUE,
    active_slot smallint CHECK (active_slot = 1),
    status text NOT NULL DEFAULT 'starting'
        CHECK (status IN ('starting', 'running', 'reconciling', 'completed', 'failed', 'canceled')),
    codex_submission_id text,
    confirmed_codex_turn_id text,
    append_count integer NOT NULL DEFAULT 0,
    max_append_count integer NOT NULL DEFAULT 5,
    started_at timestamptz NOT NULL DEFAULT now(),
    heartbeat_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    error_code text,
    error_message text,
    UNIQUE(primary_intent_id, attempt),
    UNIQUE(control_id, active_slot)
);
CREATE INDEX codex_runs_intent ON codex_turn_runs(primary_intent_id, attempt);

CREATE TABLE discord_turn_contributors (
    run_id uuid NOT NULL REFERENCES codex_turn_runs(id) ON DELETE CASCADE,
    conversation_id uuid NOT NULL REFERENCES discord_conversations(id) ON DELETE CASCADE,
    external_turn_id text,
    discord_user_id text NOT NULL,
    first_message_id text NOT NULL REFERENCES discord_input_messages(message_id),
    github_binding_id uuid REFERENCES discord_identity_bindings(id),
    github_user_id bigint,
    github_login text,
    binding_version bigint,
    contributed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(run_id, discord_user_id)
);

CREATE TABLE agent_events (
    id bigserial PRIMARY KEY,
    control_id uuid NOT NULL REFERENCES codex_thread_controls(id) ON DELETE CASCADE,
    intent_id uuid REFERENCES codex_turn_intents(id) ON DELETE CASCADE,
    run_id uuid REFERENCES codex_turn_runs(id) ON DELETE CASCADE,
    event_type text NOT NULL,
    external_event_id text,
    payload jsonb NOT NULL,
    occurred_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX agent_events_control ON agent_events(control_id, id);
CREATE INDEX agent_events_run ON agent_events(run_id, id);

CREATE TABLE tool_calls (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id uuid NOT NULL REFERENCES codex_turn_runs(id) ON DELETE CASCADE,
    intent_id uuid NOT NULL REFERENCES codex_turn_intents(id) ON DELETE CASCADE,
    thread_id text NOT NULL,
    turn_id text NOT NULL,
    call_id text NOT NULL,
    namespace text NOT NULL,
    tool text NOT NULL,
    arguments jsonb NOT NULL,
    result jsonb,
    status text NOT NULL DEFAULT 'running'
        CHECK (status IN ('running', 'reconciling', 'completed', 'failed')),
    error text,
    started_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    UNIQUE(thread_id, turn_id, call_id)
);
