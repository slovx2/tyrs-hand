CREATE TABLE desktop_turn_images (
    id uuid PRIMARY KEY,
    intent_id uuid NOT NULL REFERENCES codex_turn_intents(id) ON DELETE CASCADE,
    ordinal integer NOT NULL CHECK (ordinal >= 0 AND ordinal < 10),
    original_filename text NOT NULL,
    discord_filename text,
    media_type text,
    size_bytes bigint,
    sha256 char(64),
    status text NOT NULL CHECK (status IN ('pending','delivered','failed')),
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    discord_attachment_id text,
    error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    delivered_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(intent_id, ordinal)
);

ALTER TABLE execution_nodes ALTER COLUMN protocol_version SET DEFAULT 17;
UPDATE execution_nodes
SET protocol_version = 17, status = 'pending', last_error = NULL, updated_at = now();
