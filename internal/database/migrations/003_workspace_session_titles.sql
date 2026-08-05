ALTER TABLE workers ALTER COLUMN protocol_version SET DEFAULT 23;

CREATE TABLE workspace_session_title_tasks (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    session_id uuid NOT NULL UNIQUE REFERENCES workspace_sessions(id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES worker_workspaces(id) ON DELETE CASCADE,
    first_message_id uuid NOT NULL REFERENCES session_messages(id) ON DELETE CASCADE,
    first_message_text text NOT NULL,
    title_revision bigint NOT NULL,
    status text NOT NULL DEFAULT 'pending',
    attempt_count integer NOT NULL DEFAULT 0,
    lease_owner uuid REFERENCES workers(id) ON DELETE SET NULL,
    lease_token_hash text,
    lease_expires_at timestamptz,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    last_error_code text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    CONSTRAINT workspace_session_title_tasks_revision_check CHECK (title_revision >= 0),
    CONSTRAINT workspace_session_title_tasks_attempt_check CHECK (attempt_count BETWEEN 0 AND 3),
    CONSTRAINT workspace_session_title_tasks_status_check CHECK (
        status IN ('pending', 'claimed', 'completed', 'failed')
    )
);

CREATE INDEX workspace_session_title_tasks_claim
    ON workspace_session_title_tasks(workspace_id, status, next_attempt_at, created_at);
