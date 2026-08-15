ALTER TABLE workers ALTER COLUMN protocol_version SET DEFAULT 28;
UPDATE workers SET protocol_version = 28 WHERE protocol_version = 27;

ALTER TABLE workers
    ADD COLUMN ssh_host_key_fingerprint text;

ALTER TABLE workers
    ADD CONSTRAINT workers_ssh_host_key_fingerprint_check CHECK (
        ssh_host_key_fingerprint IS NULL OR
        ssh_host_key_fingerprint ~ '^SHA256:[A-Za-z0-9+/]{43}$'
    );

CREATE UNIQUE INDEX workers_ssh_host_key_fingerprint_unique
    ON workers(ssh_host_key_fingerprint)
    WHERE ssh_host_key_fingerprint IS NOT NULL;

ALTER TABLE client_device_pairings
    ADD COLUMN worker_id uuid REFERENCES workers(id) ON DELETE CASCADE,
    ADD COLUMN ssh_host_key_fingerprint text;

ALTER TABLE client_device_pairings
    ADD CONSTRAINT client_device_pairings_machine_check CHECK (
        (worker_id IS NULL AND ssh_host_key_fingerprint IS NULL) OR
        (worker_id IS NOT NULL AND
            ssh_host_key_fingerprint ~ '^SHA256:[A-Za-z0-9+/]{43}$')
    );

CREATE INDEX client_device_pairings_worker
    ON client_device_pairings(worker_id, created_at DESC)
    WHERE worker_id IS NOT NULL;

CREATE TABLE client_device_workers (
    device_id uuid NOT NULL REFERENCES client_devices(id) ON DELETE CASCADE,
    worker_id uuid NOT NULL REFERENCES workers(id) ON DELETE CASCADE,
    ssh_host_key_fingerprint text NOT NULL,
    approved_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(device_id, worker_id),
    CONSTRAINT client_device_workers_fingerprint_check CHECK (
        ssh_host_key_fingerprint ~ '^SHA256:[A-Za-z0-9+/]{43}$'
    )
);

CREATE INDEX client_device_workers_worker
    ON client_device_workers(worker_id, device_id);
