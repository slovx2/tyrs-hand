ALTER TABLE scheduled_tasks ADD COLUMN engine text NOT NULL DEFAULT 'codex'
    CHECK (engine IN ('codex', 'claude-code'));
ALTER TABLE scheduled_tasks ADD CONSTRAINT scheduled_tasks_session_engine_fkey
    FOREIGN KEY (target_session_id, engine) REFERENCES workspace_sessions(id, engine);
CREATE TRIGGER scheduled_task_engine_immutable BEFORE UPDATE OF engine ON scheduled_tasks
    FOR EACH ROW EXECUTE FUNCTION reject_session_engine_change();
ALTER TABLE codex_thread_controls ADD CONSTRAINT codex_controls_github_engine_check
    CHECK (source_type <> 'github_work_item' OR engine = 'codex');
