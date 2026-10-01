-- 只扩展引擎集合，保留运行时复合外键与引擎不可变约束。
ALTER TABLE worker_runtimes DROP CONSTRAINT worker_runtimes_engine_check,
    ADD CONSTRAINT worker_runtimes_engine_check CHECK (engine IN ('codex','claude-code','pi'));
ALTER TABLE workspace_sessions DROP CONSTRAINT workspace_sessions_engine_check,
    ADD CONSTRAINT workspace_sessions_engine_check CHECK (engine IN ('codex','claude-code','pi'));
ALTER TABLE codex_thread_controls DROP CONSTRAINT codex_thread_controls_engine_check,
    ADD CONSTRAINT codex_thread_controls_engine_check CHECK (engine IN ('codex','claude-code','pi'));
ALTER TABLE desktop_thread_requests DROP CONSTRAINT desktop_thread_requests_engine_check,
    ADD CONSTRAINT desktop_thread_requests_engine_check CHECK (engine IN ('codex','claude-code','pi'));
ALTER TABLE scheduled_tasks DROP CONSTRAINT scheduled_tasks_engine_check,
    ADD CONSTRAINT scheduled_tasks_engine_check CHECK (engine IN ('codex','claude-code','pi'));
ALTER TABLE client_device_workers DROP CONSTRAINT client_device_workers_engine_check,
    ADD CONSTRAINT client_device_workers_engine_check CHECK (engine IN ('codex','claude-code','pi'));
ALTER TABLE client_device_pairings DROP CONSTRAINT client_device_pairings_engine_check,
    ADD CONSTRAINT client_device_pairings_engine_check CHECK (engine IN ('codex','claude-code','pi'));
ALTER TABLE discord_forums DROP CONSTRAINT discord_forums_default_engine_check,
    ADD CONSTRAINT discord_forums_default_engine_check CHECK (default_engine IN ('codex','claude-code','pi'));
ALTER TABLE discord_conversations DROP CONSTRAINT discord_conversations_engine_check,
    ADD CONSTRAINT discord_conversations_engine_check CHECK (engine IN ('codex','claude-code','pi'));
ALTER TABLE discord_user_codex_preferences DROP CONSTRAINT discord_user_codex_preferences_engine_check,
    ADD CONSTRAINT discord_user_codex_preferences_engine_check CHECK (engine IN ('codex','claude-code','pi'));
