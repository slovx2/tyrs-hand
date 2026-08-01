ALTER TABLE codex_turn_runs
    ADD COLUMN codex_error jsonb;

ALTER TABLE execution_nodes
    ALTER COLUMN protocol_version SET DEFAULT 18;

UPDATE execution_nodes
SET protocol_version = 18, status = 'pending', last_error = NULL, updated_at = now();
