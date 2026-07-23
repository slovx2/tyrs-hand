ALTER TABLE codex_thread_controls
    ADD COLUMN app_server_settings_generation bigint NOT NULL DEFAULT 0
        CHECK (app_server_settings_generation >= 0),
    ADD COLUMN app_server_settings_sequence bigint NOT NULL DEFAULT 0
        CHECK (app_server_settings_sequence >= 0),
    DROP CONSTRAINT IF EXISTS codex_thread_controls_service_tier_check;

ALTER TABLE discord_conversations
    DROP CONSTRAINT IF EXISTS discord_conversations_service_tier_check,
    ALTER COLUMN service_tier DROP NOT NULL,
    ALTER COLUMN service_tier DROP DEFAULT;

ALTER TABLE execution_nodes ALTER COLUMN protocol_version SET DEFAULT 6;
UPDATE execution_nodes SET protocol_version = 6, status = 'pending', last_error = NULL;
