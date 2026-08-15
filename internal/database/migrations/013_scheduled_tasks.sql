CREATE TABLE scheduled_tasks (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES worker_workspaces(id) ON DELETE CASCADE,
    workspace_project_id uuid NOT NULL REFERENCES workspace_projects(id) ON DELETE CASCADE,
    target_session_id uuid REFERENCES workspace_sessions(id) ON DELETE CASCADE,
    created_by_administrator_id uuid REFERENCES administrators(id) ON DELETE SET NULL,
    kind text NOT NULL,
    name text NOT NULL,
    prompt text NOT NULL,
    status text NOT NULL DEFAULT 'active',
    schedule_text text NOT NULL,
    timezone text NOT NULL,
    schedule_kind text NOT NULL,
    interval_seconds bigint,
    next_run_at timestamptz,
    blocked_until timestamptz,
    last_run_at timestamptz,
    agent_profile_id uuid REFERENCES agent_profiles(id) ON DELETE RESTRICT,
    model text,
    reasoning_effort text,
    service_tier text,
    schedule_revision bigint NOT NULL DEFAULT 1,
    last_error_code text,
    last_error_message text,
    deleted_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT scheduled_tasks_kind_check CHECK (kind IN ('standalone','heartbeat')),
    CONSTRAINT scheduled_tasks_status_check CHECK (status IN ('active','paused','completed','deleted')),
    CONSTRAINT scheduled_tasks_schedule_kind_check CHECK (schedule_kind IN ('interval','wall_clock')),
    CONSTRAINT scheduled_tasks_revision_check CHECK (schedule_revision > 0),
    CONSTRAINT scheduled_tasks_interval_check CHECK (interval_seconds IS NULL OR interval_seconds > 0),
    CONSTRAINT scheduled_tasks_target_check CHECK (
        (kind='standalone' AND target_session_id IS NULL AND agent_profile_id IS NOT NULL) OR
        (kind='heartbeat' AND target_session_id IS NOT NULL AND agent_profile_id IS NULL AND
            model IS NULL AND reasoning_effort IS NULL AND service_tier IS NULL)
    )
);

CREATE UNIQUE INDEX scheduled_tasks_active_heartbeat
    ON scheduled_tasks(target_session_id)
    WHERE kind='heartbeat' AND status='active';

CREATE INDEX scheduled_tasks_due
    ON scheduled_tasks(next_run_at, workspace_id)
    WHERE status='active' AND next_run_at IS NOT NULL;

CREATE TABLE scheduled_task_runs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    scheduled_task_id uuid NOT NULL REFERENCES scheduled_tasks(id) ON DELETE CASCADE,
    schedule_revision bigint NOT NULL,
    trigger text NOT NULL,
    trigger_key text NOT NULL,
    scheduled_for timestamptz NOT NULL,
    coalesced_through timestamptz,
    status text NOT NULL DEFAULT 'queued',
    intent_id uuid REFERENCES codex_turn_intents(id) ON DELETE SET NULL,
    session_id uuid REFERENCES workspace_sessions(id) ON DELETE SET NULL,
    task_snapshot jsonb NOT NULL DEFAULT '{}'::jsonb,
    error_code text,
    error_message text,
    started_at timestamptz,
    finished_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT scheduled_task_runs_trigger_check CHECK (trigger IN ('scheduled','run_now')),
    CONSTRAINT scheduled_task_runs_status_check CHECK (
        status IN ('queued','running','waiting_for_user','succeeded','failed','canceled')
    ),
    CONSTRAINT scheduled_task_runs_revision_check CHECK (schedule_revision > 0),
    UNIQUE(scheduled_task_id, trigger_key)
);

CREATE UNIQUE INDEX scheduled_task_runs_one_active
    ON scheduled_task_runs(scheduled_task_id)
    WHERE status IN ('queued','running','waiting_for_user');

CREATE INDEX scheduled_task_runs_task_history
    ON scheduled_task_runs(scheduled_task_id, created_at DESC);

CREATE UNIQUE INDEX scheduled_task_runs_intent
    ON scheduled_task_runs(intent_id)
    WHERE intent_id IS NOT NULL;

CREATE OR REPLACE FUNCTION project_scheduled_task_run_status()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    projected_status text;
    affected_task_id uuid;
BEGIN
    projected_status := CASE NEW.status
        WHEN 'placement_pending' THEN 'queued'
        WHEN 'queued' THEN 'queued'
        WHEN 'reconciling' THEN 'queued'
        WHEN 'retry_wait' THEN 'queued'
        WHEN 'dispatching' THEN 'running'
        WHEN 'awaiting_confirmation' THEN 'running'
        WHEN 'running' THEN 'running'
        WHEN 'waiting_for_user' THEN 'waiting_for_user'
        WHEN 'completed' THEN 'succeeded'
        WHEN 'failed' THEN 'failed'
        WHEN 'canceled' THEN 'canceled'
        ELSE NULL
    END;
    IF projected_status IS NULL THEN
        RETURN NEW;
    END IF;
    UPDATE scheduled_task_runs SET
        status=projected_status,
        started_at=CASE WHEN projected_status='running' THEN COALESCE(started_at,now()) ELSE started_at END,
        finished_at=CASE WHEN projected_status IN ('succeeded','failed','canceled') THEN now() ELSE NULL END,
        error_code=CASE WHEN projected_status IN ('failed','canceled') THEN NEW.last_error_code ELSE NULL END,
        error_message=CASE WHEN projected_status IN ('failed','canceled') THEN NEW.last_error_message ELSE NULL END,
        updated_at=now()
    WHERE intent_id=NEW.id
      AND status NOT IN ('succeeded','failed','canceled')
    RETURNING scheduled_task_id INTO affected_task_id;
    IF affected_task_id IS NOT NULL AND
       projected_status IN ('succeeded','failed','canceled') THEN
        UPDATE scheduled_tasks SET
            last_error_code=CASE WHEN projected_status IN ('failed','canceled')
                THEN NEW.last_error_code ELSE NULL END,
            last_error_message=CASE WHEN projected_status IN ('failed','canceled')
                THEN NEW.last_error_message ELSE NULL END,
            updated_at=now()
        WHERE id=affected_task_id;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER codex_intent_scheduled_run_status
AFTER UPDATE OF status ON codex_turn_intents
FOR EACH ROW WHEN (OLD.status IS DISTINCT FROM NEW.status)
EXECUTE FUNCTION project_scheduled_task_run_status();
