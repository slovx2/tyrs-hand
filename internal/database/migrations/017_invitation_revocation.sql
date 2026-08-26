ALTER TABLE administrator_invitations
    ADD COLUMN IF NOT EXISTS revoked_at timestamptz;

CREATE INDEX IF NOT EXISTS administrator_invitations_status
    ON administrator_invitations (created_at DESC)
    WHERE consumed_at IS NULL AND revoked_at IS NULL;
