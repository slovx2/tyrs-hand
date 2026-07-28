CREATE TABLE discord_turn_status_cards (
    run_id uuid NOT NULL REFERENCES codex_turn_runs(id) ON DELETE CASCADE,
    guild_id text NOT NULL,
    projection_key text NOT NULL,
    revision bigint NOT NULL CHECK (revision >= 0),
    role text NOT NULL CHECK (role IN ('pending', 'current', 'history')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(run_id, projection_key),
    UNIQUE(run_id, revision),
    FOREIGN KEY(guild_id, projection_key)
        REFERENCES discord_projections(guild_id, projection_key) ON DELETE CASCADE
);

CREATE UNIQUE INDEX discord_turn_status_cards_current
    ON discord_turn_status_cards(run_id) WHERE role = 'current';
CREATE INDEX discord_turn_status_cards_pending
    ON discord_turn_status_cards(guild_id, projection_key) WHERE role = 'pending';

ALTER TABLE execution_nodes ALTER COLUMN protocol_version SET DEFAULT 14;
UPDATE execution_nodes
SET protocol_version = 14, status = 'pending', last_error = NULL, updated_at = now();
