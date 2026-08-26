ALTER TABLE administrators
    ADD COLUMN IF NOT EXISTS role text NOT NULL DEFAULT 'admin',
    ADD COLUMN IF NOT EXISTS enabled boolean NOT NULL DEFAULT true;

ALTER TABLE administrators
    DROP CONSTRAINT IF EXISTS administrators_role_check;
ALTER TABLE administrators
    ADD CONSTRAINT administrators_role_check CHECK (role IN ('admin', 'user'));

CREATE TABLE IF NOT EXISTS administrator_invitations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash char(64) NOT NULL UNIQUE,
    username text NOT NULL,
    expires_at timestamptz NOT NULL,
    created_by uuid NOT NULL REFERENCES administrators(id) ON DELETE CASCADE,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS administrator_invitations_active
    ON administrator_invitations (expires_at)
    WHERE consumed_at IS NULL;

CREATE TABLE IF NOT EXISTS worker_administrators (
    worker_id uuid NOT NULL REFERENCES workers(id) ON DELETE CASCADE,
    administrator_id uuid NOT NULL REFERENCES administrators(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (worker_id, administrator_id)
);

CREATE INDEX IF NOT EXISTS worker_administrators_administrator
    ON worker_administrators (administrator_id, worker_id);

UPDATE administrators SET role = 'admin' WHERE role IS NULL OR role = '';
