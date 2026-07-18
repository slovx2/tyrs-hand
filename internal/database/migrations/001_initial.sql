CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE administrators (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    username text NOT NULL UNIQUE,
    password_hash text NOT NULL,
    totp_secret_ciphertext bytea NOT NULL,
    recovery_codes_hash jsonb NOT NULL DEFAULT '[]'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE admin_sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    administrator_id uuid NOT NULL REFERENCES administrators(id) ON DELETE CASCADE,
    token_hash char(64) NOT NULL UNIQUE,
    csrf_token_hash char(64) NOT NULL,
    expires_at timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE encrypted_secrets (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    secret_key text NOT NULL UNIQUE,
    key_version integer NOT NULL DEFAULT 1,
    nonce bytea NOT NULL,
    ciphertext bytea NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE platform_settings (
    setting_key text PRIMARY KEY,
    value jsonb NOT NULL,
    version bigint NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE github_app_configs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    app_id bigint NOT NULL UNIQUE,
    client_id text,
    app_slug text NOT NULL,
    private_key_secret_id uuid NOT NULL REFERENCES encrypted_secrets(id),
    webhook_secret_id uuid NOT NULL REFERENCES encrypted_secrets(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE scm_installations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    provider text NOT NULL,
    external_id bigint NOT NULL,
    account_login text NOT NULL,
    account_type text NOT NULL,
    suspended_at timestamptz,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(provider, external_id)
);

CREATE TABLE repositories (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    installation_id uuid NOT NULL REFERENCES scm_installations(id) ON DELETE CASCADE,
    provider text NOT NULL,
    external_id bigint NOT NULL,
    owner text NOT NULL,
    name text NOT NULL,
    default_branch text NOT NULL,
    clone_url text NOT NULL,
    enabled boolean NOT NULL DEFAULT true,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(provider, external_id),
    UNIQUE(provider, owner, name)
);

CREATE TABLE agent_profiles (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL UNIQUE,
    provider text NOT NULL DEFAULT 'codex',
    model text,
    reasoning_effort text,
    service_tier text,
    sandbox text NOT NULL DEFAULT 'workspace-write',
    network_enabled boolean NOT NULL DEFAULT true,
    approval_policy text NOT NULL DEFAULT 'never',
    allowed_tools jsonb NOT NULL DEFAULT '[]'::jsonb,
    config jsonb NOT NULL DEFAULT '{}'::jsonb,
    context_version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE trigger_rules (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    repository_id uuid NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    agent_profile_id uuid NOT NULL REFERENCES agent_profiles(id),
    name text NOT NULL,
    event_name text NOT NULL,
    action text,
    enabled boolean NOT NULL DEFAULT true,
    priority integer NOT NULL DEFAULT 100,
    actor_min_permission text NOT NULL DEFAULT 'triage',
    mention_required boolean NOT NULL DEFAULT false,
    instruction_template text NOT NULL,
    skills jsonb NOT NULL DEFAULT '[]'::jsonb,
    allowed_tools jsonb NOT NULL DEFAULT '[]'::jsonb,
    dangerous_actions jsonb NOT NULL DEFAULT '[]'::jsonb,
    filters jsonb NOT NULL DEFAULT '{}'::jsonb,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(repository_id, name)
);

CREATE TABLE webhook_deliveries (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    provider text NOT NULL,
    delivery_id text NOT NULL,
    event_name text NOT NULL,
    action text,
    signature_valid boolean NOT NULL,
    payload jsonb NOT NULL,
    status text NOT NULL DEFAULT 'received',
    error text,
    received_at timestamptz NOT NULL DEFAULT now(),
    processed_at timestamptz,
    UNIQUE(provider, delivery_id)
);

CREATE TABLE work_items (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    repository_id uuid NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    kind text NOT NULL,
    external_number integer NOT NULL,
    title text NOT NULL DEFAULT '',
    state text NOT NULL DEFAULT 'open',
    agent_owned boolean NOT NULL DEFAULT false,
    base_sha text,
    head_sha text,
    context_version bigint NOT NULL DEFAULT 1,
    closed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(repository_id, kind, external_number)
);

CREATE TABLE work_item_channels (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    work_item_id uuid NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    channel_type text NOT NULL,
    external_number integer NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(work_item_id, channel_type, external_number)
);

CREATE TABLE work_item_memories (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    work_item_id uuid NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    scope text NOT NULL DEFAULT 'work_item',
    summary text NOT NULL,
    source_thread_id text,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE agent_threads (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    work_item_id uuid NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    agent_profile_id uuid NOT NULL REFERENCES agent_profiles(id),
    provider text NOT NULL,
    external_thread_id text NOT NULL,
    context_version bigint NOT NULL,
    codex_home_key text NOT NULL,
    rollout_path text,
    status text NOT NULL DEFAULT 'active',
    last_turn_id text,
    last_used_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(work_item_id, agent_profile_id, context_version),
    UNIQUE(provider, external_thread_id)
);

CREATE TABLE job_intents (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    work_item_id uuid NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    repository_id uuid NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    agent_profile_id uuid NOT NULL REFERENCES agent_profiles(id),
    webhook_delivery_id uuid REFERENCES webhook_deliveries(id),
    idempotency_key text NOT NULL UNIQUE,
    status text NOT NULL DEFAULT 'queued',
    instruction text NOT NULL,
    skills jsonb NOT NULL DEFAULT '[]'::jsonb,
    allowed_tools jsonb NOT NULL DEFAULT '[]'::jsonb,
    priority integer NOT NULL DEFAULT 100,
    available_at timestamptz NOT NULL DEFAULT now(),
    attempt_count integer NOT NULL DEFAULT 0,
    max_attempts integer NOT NULL DEFAULT 3,
    lease_token char(64),
    lease_epoch bigint NOT NULL DEFAULT 0,
    lease_expires_at timestamptz,
    worker_id text,
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE job_attempts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    job_id uuid NOT NULL REFERENCES job_intents(id) ON DELETE CASCADE,
    attempt integer NOT NULL,
    worker_id text NOT NULL,
    lease_epoch bigint NOT NULL,
    capability_hash char(64) NOT NULL UNIQUE,
    started_at timestamptz NOT NULL DEFAULT now(),
    heartbeat_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    status text NOT NULL DEFAULT 'running',
    error text,
    UNIQUE(job_id, attempt)
);

CREATE TABLE worker_nodes (
    id text PRIMARY KEY,
    version text NOT NULL,
    status text NOT NULL DEFAULT 'online',
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    heartbeat_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE repo_caches (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    repository_id uuid NOT NULL UNIQUE REFERENCES repositories(id) ON DELETE CASCADE,
    path text NOT NULL UNIQUE,
    status text NOT NULL DEFAULT 'ready',
    size_bytes bigint NOT NULL DEFAULT 0,
    last_fetch_at timestamptz,
    last_used_at timestamptz NOT NULL DEFAULT now(),
    error text
);

CREATE TABLE worktrees (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    work_item_id uuid NOT NULL UNIQUE REFERENCES work_items(id) ON DELETE CASCADE,
    repo_cache_id uuid NOT NULL REFERENCES repo_caches(id) ON DELETE CASCADE,
    path text NOT NULL UNIQUE,
    branch text NOT NULL,
    base_sha text NOT NULL,
    head_sha text NOT NULL,
    status text NOT NULL DEFAULT 'ready',
    dirty boolean NOT NULL DEFAULT false,
    last_used_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz,
    error text
);

CREATE TABLE agent_events (
    id bigserial PRIMARY KEY,
    thread_id uuid REFERENCES agent_threads(id) ON DELETE CASCADE,
    job_id uuid REFERENCES job_intents(id) ON DELETE CASCADE,
    event_type text NOT NULL,
    external_event_id text,
    payload jsonb NOT NULL,
    occurred_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE tool_calls (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    job_attempt_id uuid NOT NULL REFERENCES job_attempts(id) ON DELETE CASCADE,
    thread_id text NOT NULL,
    turn_id text NOT NULL,
    call_id text NOT NULL,
    namespace text NOT NULL,
    tool text NOT NULL,
    arguments jsonb NOT NULL,
    result jsonb,
    status text NOT NULL DEFAULT 'running',
    error text,
    started_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    UNIQUE(thread_id, turn_id, call_id)
);

CREATE TABLE audit_logs (
    id bigserial PRIMARY KEY,
    administrator_id uuid REFERENCES administrators(id),
    action text NOT NULL,
    resource_type text NOT NULL,
    resource_id text,
    request_id text,
    ip_address inet,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_jobs_claim ON job_intents(status, available_at, priority, created_at);
CREATE INDEX idx_jobs_work_item ON job_intents(work_item_id, created_at);
CREATE INDEX idx_agent_events_thread ON agent_events(thread_id, id);
CREATE INDEX idx_webhook_received ON webhook_deliveries(status, received_at);
CREATE INDEX idx_sessions_expiry ON admin_sessions(expires_at);
CREATE INDEX idx_worktrees_expiry ON worktrees(expires_at) WHERE expires_at IS NOT NULL;
