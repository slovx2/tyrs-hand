CREATE TABLE official_thread_bindings (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES worker_workspaces(id) ON DELETE CASCADE,
    conversation_id uuid REFERENCES discord_conversations(id) ON DELETE CASCADE,
    workspace_project_id uuid REFERENCES workspace_projects(id) ON DELETE SET NULL,
    thread_id text NOT NULL,
    lifecycle_state text NOT NULL DEFAULT 'active'
        CHECK (lifecycle_state IN ('active', 'archived')),
    interactive_owner text NOT NULL DEFAULT 'external'
        CHECK (interactive_owner IN ('control', 'external')),
    owned_turn_id text,
    last_client_message_id text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, thread_id),
    UNIQUE (conversation_id)
);

INSERT INTO official_thread_bindings(
    workspace_id, conversation_id, workspace_project_id, thread_id, lifecycle_state)
SELECT DISTINCT ON (control.discord_conversation_id)
    control.workspace_id, control.discord_conversation_id,
    conversation.workspace_project_id, control.external_thread_id,
    CASE WHEN conversation.lifecycle_state = 'archived' THEN 'archived' ELSE 'active' END
FROM codex_thread_controls control
JOIN discord_conversations conversation ON conversation.id=control.discord_conversation_id
WHERE control.source_type='workspace_session'
  AND control.workspace_id IS NOT NULL
  AND control.external_thread_id IS NOT NULL
ORDER BY control.discord_conversation_id, control.updated_at DESC, control.id DESC
ON CONFLICT DO NOTHING;

CREATE TABLE official_thread_projections (
    workspace_id uuid NOT NULL REFERENCES worker_workspaces(id) ON DELETE CASCADE,
    thread_id text NOT NULL,
    thread jsonb NOT NULL,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    observed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, thread_id)
);

CREATE TABLE official_plan_actions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES worker_workspaces(id) ON DELETE CASCADE,
    conversation_id uuid NOT NULL REFERENCES discord_conversations(id) ON DELETE CASCADE,
    thread_id text NOT NULL,
    turn_id text NOT NULL,
    item_id text NOT NULL,
    plan_text text NOT NULL,
    status text NOT NULL DEFAULT 'available'
        CHECK (status IN ('available', 'executed', 'stale')),
    created_at timestamptz NOT NULL DEFAULT now(),
    executed_at timestamptz,
    UNIQUE (workspace_id, thread_id, turn_id, item_id)
);

CREATE TABLE official_turn_submissions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES worker_workspaces(id) ON DELETE CASCADE,
    conversation_id uuid NOT NULL REFERENCES discord_conversations(id) ON DELETE CASCADE,
    plan_action_id uuid REFERENCES official_plan_actions(id) ON DELETE SET NULL,
    source_type text NOT NULL CHECK (source_type IN ('discord_message', 'discord_plan')),
    source_order numeric(20,0) NOT NULL,
    discord_message_id text,
    client_user_message_id text NOT NULL,
    instruction text NOT NULL,
    display_instruction text NOT NULL DEFAULT '',
    input jsonb NOT NULL DEFAULT '[]'::jsonb,
    preferences jsonb NOT NULL DEFAULT '{}'::jsonb,
    status text NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'submitting', 'ambiguous', 'submitted', 'failed', 'canceled')),
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0 AND attempt_count <= 2),
    available_at timestamptz NOT NULL DEFAULT now(),
    lease_token_hash char(64),
    lease_expires_at timestamptz,
    thread_id text,
    turn_id text,
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    submitted_at timestamptz,
    UNIQUE (workspace_id, client_user_message_id),
    UNIQUE (conversation_id, source_order)
);

CREATE INDEX official_turn_submissions_queue_idx
    ON official_turn_submissions(workspace_id, source_order, created_at, id)
    WHERE status IN ('queued', 'submitting', 'ambiguous');

CREATE TABLE official_thread_actions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES worker_workspaces(id) ON DELETE CASCADE,
    conversation_id uuid NOT NULL REFERENCES discord_conversations(id) ON DELETE CASCADE,
    source_order numeric(20,0) NOT NULL,
    idempotency_key text NOT NULL UNIQUE,
    action text NOT NULL CHECK (action IN ('interrupt', 'archive', 'unarchive')),
    status text NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'applying', 'completed', 'failed')),
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0 AND attempt_count <= 3),
    available_at timestamptz NOT NULL DEFAULT now(),
    lease_token_hash char(64),
    lease_expires_at timestamptz,
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    UNIQUE (conversation_id, source_order)
);

CREATE INDEX official_thread_actions_queue_idx
    ON official_thread_actions(workspace_id, source_order, created_at, id)
    WHERE status IN ('queued', 'applying');

ALTER TABLE discord_input_messages
    ADD COLUMN official_submission_id uuid REFERENCES official_turn_submissions(id) ON DELETE SET NULL;

CREATE TABLE official_submission_attachments (
    submission_id uuid NOT NULL REFERENCES official_turn_submissions(id) ON DELETE CASCADE,
    attachment_id uuid NOT NULL REFERENCES discord_attachments(id) ON DELETE CASCADE,
    ordinal integer NOT NULL CHECK (ordinal >= 0 AND ordinal < 10),
    materialization_id uuid REFERENCES client_materializations(id) ON DELETE SET NULL,
    PRIMARY KEY (submission_id, attachment_id),
    UNIQUE (submission_id, ordinal)
);

CREATE TABLE official_server_requests (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES worker_workspaces(id) ON DELETE CASCADE,
    conversation_id uuid REFERENCES discord_conversations(id) ON DELETE CASCADE,
    connection_id uuid NOT NULL,
    request_key char(64) NOT NULL UNIQUE,
    app_server_request_id jsonb NOT NULL,
    method text NOT NULL,
    thread_id text NOT NULL,
    turn_id text,
    item_id text,
    params jsonb NOT NULL,
    owner text NOT NULL CHECK (owner IN ('control', 'external')),
    status text NOT NULL DEFAULT 'observed'
        CHECK (status IN ('observed', 'pending', 'answered', 'dismissed', 'resolved', 'stale')),
    response jsonb,
    draft_answers jsonb NOT NULL DEFAULT '{}'::jsonb,
    answer_surface text CHECK (answer_surface IN ('discord', 'dismissed', 'external')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    resolved_at timestamptz
);

CREATE INDEX official_server_requests_pending_idx
    ON official_server_requests(workspace_id, thread_id, created_at, id)
    WHERE status='pending';

ALTER TABLE integration_outbox ADD COLUMN predecessor_operation_key text;
CREATE INDEX integration_outbox_predecessor_idx
    ON integration_outbox(integration, predecessor_operation_key)
    WHERE predecessor_operation_key IS NOT NULL;
