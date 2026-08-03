ALTER TABLE codex_runtime_settings
    DROP CONSTRAINT IF EXISTS codex_runtime_settings_reasoning_effort_check;
ALTER TABLE discord_user_codex_preferences
    DROP CONSTRAINT IF EXISTS discord_user_codex_preferences_reasoning_effort_check;
ALTER TABLE development_sessions
    DROP CONSTRAINT IF EXISTS development_sessions_reasoning_effort_check;
ALTER TABLE client_user_preferences
    DROP CONSTRAINT IF EXISTS client_user_preferences_reasoning_effort_check;

ALTER TABLE execution_nodes ALTER COLUMN protocol_version SET DEFAULT 21;
UPDATE execution_nodes
SET protocol_version = 21, status = 'pending', last_error = NULL, updated_at = now();
