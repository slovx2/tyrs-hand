CREATE TABLE ssh_credentials (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL UNIQUE,
    secret_id uuid NOT NULL UNIQUE REFERENCES encrypted_secrets(id) ON DELETE RESTRICT,
    public_key text NOT NULL,
    fingerprint text NOT NULL UNIQUE,
    enabled boolean NOT NULL DEFAULT true,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE ssh_hosts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    alias text NOT NULL UNIQUE,
    hostname text NOT NULL,
    port integer NOT NULL DEFAULT 22 CHECK (port BETWEEN 1 AND 65535),
    username text NOT NULL,
    credential_id uuid NOT NULL REFERENCES ssh_credentials(id) ON DELETE RESTRICT,
    proxy_jump_host_id uuid REFERENCES ssh_hosts(id) ON DELETE RESTRICT,
    enabled boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (proxy_jump_host_id IS NULL OR proxy_jump_host_id <> id)
);

CREATE TABLE ssh_host_execution_nodes (
    host_id uuid NOT NULL REFERENCES ssh_hosts(id) ON DELETE CASCADE,
    execution_node_id uuid NOT NULL REFERENCES execution_nodes(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (host_id, execution_node_id)
);

CREATE INDEX ssh_hosts_credential ON ssh_hosts(credential_id);
CREATE INDEX ssh_hosts_proxy_jump ON ssh_hosts(proxy_jump_host_id)
    WHERE proxy_jump_host_id IS NOT NULL;
CREATE INDEX ssh_host_nodes_execution_node
    ON ssh_host_execution_nodes(execution_node_id, host_id);
