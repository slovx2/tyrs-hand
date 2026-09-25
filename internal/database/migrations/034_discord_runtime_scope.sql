ALTER TABLE discord_forums ADD COLUMN default_engine text NOT NULL DEFAULT 'codex'
    CHECK (default_engine IN ('codex','claude-code'));
ALTER TABLE discord_conversations ADD COLUMN engine text NOT NULL DEFAULT 'codex'
    CHECK (engine IN ('codex','claude-code'));
UPDATE discord_conversations conversation SET engine=session.engine
    FROM workspace_sessions session WHERE session.id=conversation.session_id;
ALTER TABLE discord_conversations ADD UNIQUE(id,engine);
ALTER TABLE discord_conversations ADD CONSTRAINT discord_conversation_session_engine_fkey
    FOREIGN KEY(session_id,engine) REFERENCES workspace_sessions(id,engine);
ALTER TABLE codex_thread_controls ADD CONSTRAINT codex_control_conversation_engine_fkey
    FOREIGN KEY(discord_conversation_id,engine) REFERENCES discord_conversations(id,engine);
CREATE TRIGGER discord_conversation_engine_immutable BEFORE UPDATE OF engine ON discord_conversations
    FOR EACH ROW EXECUTE FUNCTION reject_session_engine_change();

-- 模型选择不能从 Codex 偏好渗入 Claude 会话。
ALTER TABLE discord_user_codex_preferences ADD COLUMN engine text NOT NULL DEFAULT 'codex'
    CHECK (engine IN ('codex','claude-code'));
ALTER TABLE discord_user_codex_preferences DROP CONSTRAINT discord_user_codex_preferences_pkey;
ALTER TABLE discord_user_codex_preferences ADD PRIMARY KEY(guild_id,discord_user_id,engine);
