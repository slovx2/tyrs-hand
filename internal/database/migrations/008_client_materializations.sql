CREATE TABLE client_materializations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    device_id uuid REFERENCES client_devices(id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES worker_workspaces(id) ON DELETE CASCADE,
    worker_id uuid NOT NULL REFERENCES workers(id) ON DELETE CASCADE,
    source_type text NOT NULL CHECK (source_type IN ('mobile', 'discord')),
    source_key text NOT NULL,
    client_id text,
    original_filename text NOT NULL,
    media_type text NOT NULL,
    size_bytes bigint NOT NULL CHECK (size_bytes > 0 AND size_bytes <= 26214400),
    sha256 char(64) NOT NULL,
    storage_key text NOT NULL,
    status text NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'materializing', 'completed', 'failed')),
    lease_token_hash char(64),
    lease_expires_at timestamptz,
    remote_path text,
    error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    UNIQUE (source_type, source_key),
    CHECK ((source_type = 'mobile' AND device_id IS NOT NULL AND client_id IS NOT NULL)
        OR (source_type = 'discord' AND device_id IS NULL))
);

CREATE INDEX client_materializations_worker_queue_idx
    ON client_materializations(worker_id, created_at, id)
    WHERE status IN ('queued', 'materializing');
