CREATE TABLE client_device_pairings (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    administrator_id uuid NOT NULL REFERENCES administrators(id) ON DELETE CASCADE,
    pairing_secret_hash text NOT NULL UNIQUE,
    claim_token_hash text UNIQUE,
    device_id uuid,
    device_name text,
    platform text,
    credential_hash text,
    status text NOT NULL DEFAULT 'waiting_scan'
        CHECK (status IN ('waiting_scan','waiting_confirmation','approved','rejected')),
    expires_at timestamptz NOT NULL,
    claimed_at timestamptz,
    confirmed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (
        status = 'waiting_scan'
        OR (claim_token_hash IS NOT NULL AND device_id IS NOT NULL
            AND device_name IS NOT NULL AND platform IS NOT NULL
            AND credential_hash IS NOT NULL)
    )
);

CREATE UNIQUE INDEX client_device_pairings_pending_device
    ON client_device_pairings(device_id)
    WHERE device_id IS NOT NULL AND status = 'waiting_confirmation';
CREATE UNIQUE INDEX client_device_pairings_pending_credential
    ON client_device_pairings(credential_hash)
    WHERE credential_hash IS NOT NULL AND status = 'waiting_confirmation';
CREATE INDEX client_device_pairings_administrator
    ON client_device_pairings(administrator_id, created_at DESC);

CREATE TABLE client_devices (
    id uuid PRIMARY KEY,
    administrator_id uuid NOT NULL REFERENCES administrators(id) ON DELETE CASCADE,
    name text NOT NULL,
    platform text NOT NULL,
    credential_hash text NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now(),
    approved_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz
);

CREATE INDEX client_devices_administrator
    ON client_devices(administrator_id, created_at DESC);
