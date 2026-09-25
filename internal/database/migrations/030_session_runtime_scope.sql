-- 既有会话均属于 Codex；引擎在创建时固定，之后不能迁移。
ALTER TABLE workspace_sessions ADD COLUMN engine text NOT NULL DEFAULT 'codex'
    CHECK (engine IN ('codex', 'claude-code'));
ALTER TABLE codex_thread_controls ADD COLUMN engine text NOT NULL DEFAULT 'codex'
    CHECK (engine IN ('codex', 'claude-code'));
ALTER TABLE desktop_thread_requests ADD COLUMN engine text NOT NULL DEFAULT 'codex'
    CHECK (engine IN ('codex', 'claude-code'));

ALTER TABLE workspace_sessions ADD CONSTRAINT workspace_sessions_id_engine_key UNIQUE (id, engine);
ALTER TABLE codex_thread_controls ADD CONSTRAINT codex_controls_id_engine_key UNIQUE (id, engine);
ALTER TABLE codex_thread_controls ADD CONSTRAINT codex_controls_session_engine_fkey
    FOREIGN KEY (session_id, engine) REFERENCES workspace_sessions(id, engine);
ALTER TABLE desktop_thread_requests ADD CONSTRAINT desktop_requests_control_engine_fkey
    FOREIGN KEY (control_id, engine) REFERENCES codex_thread_controls(id, engine);
ALTER TABLE desktop_thread_requests ADD CONSTRAINT desktop_requests_source_engine_fkey
    FOREIGN KEY (source_control_id, engine) REFERENCES codex_thread_controls(id, engine);

DROP INDEX codex_controls_external_thread;
CREATE UNIQUE INDEX codex_controls_external_thread
    ON codex_thread_controls(worker_id, engine, external_thread_id) NULLS NOT DISTINCT
    WHERE external_thread_id IS NOT NULL;
DROP INDEX desktop_thread_requests_pending_key;
CREATE UNIQUE INDEX desktop_thread_requests_pending_key
    ON desktop_thread_requests(workspace_id, engine, request_key)
    WHERE status IN ('preparing', 'post_pending', 'codex_pending');

CREATE FUNCTION reject_session_engine_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.engine IS DISTINCT FROM OLD.engine THEN
        RAISE EXCEPTION '会话引擎不能改变' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER workspace_session_engine_immutable BEFORE UPDATE OF engine ON workspace_sessions
    FOR EACH ROW EXECUTE FUNCTION reject_session_engine_change();
CREATE TRIGGER codex_control_engine_immutable BEFORE UPDATE OF engine ON codex_thread_controls
    FOR EACH ROW EXECUTE FUNCTION reject_session_engine_change();
CREATE TRIGGER desktop_request_engine_immutable BEFORE UPDATE OF engine ON desktop_thread_requests
    FOR EACH ROW EXECUTE FUNCTION reject_session_engine_change();

-- 升级后的同键重试仍指向原提交；不能因键格式变化重放旧 Turn 或 steer。
UPDATE codex_turn_intents SET idempotency_key =
    regexp_replace(idempotency_key, '^(desktop-(turn|steer):[^:]+):', '\1:codex:')
    WHERE idempotency_key ~ '^desktop-(turn|steer):[0-9a-f-]{36}:[0-9a-f]{64}$';
UPDATE session_messages SET local_id =
    regexp_replace(local_id, '^(desktop:desktop-(turn|steer):[^:]+):', '\1:codex:')
    WHERE local_id ~ '^desktop:desktop-(turn|steer):[0-9a-f-]{36}:[0-9a-f]{64}$';
