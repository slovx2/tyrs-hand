ALTER TABLE discord_conversations
    ADD COLUMN collaboration_mode text NOT NULL DEFAULT 'default'
        CHECK (collaboration_mode IN ('default','plan')),
    ADD COLUMN collaboration_mode_revision bigint NOT NULL DEFAULT 0
        CHECK (collaboration_mode_revision >= 0);

ALTER TABLE codex_thread_controls
    ADD COLUMN collaboration_mode text NOT NULL DEFAULT 'default'
        CHECK (collaboration_mode IN ('default','plan')),
    ADD COLUMN collaboration_mode_revision bigint NOT NULL DEFAULT 0
        CHECK (collaboration_mode_revision >= 0);

ALTER TABLE codex_turn_runs
    ADD COLUMN collaboration_mode text NOT NULL DEFAULT 'default'
        CHECK (collaboration_mode IN ('default','plan'));

ALTER TABLE execution_nodes ALTER COLUMN protocol_version SET DEFAULT 12;
UPDATE execution_nodes SET protocol_version = 12, status = 'pending', last_error = NULL;
