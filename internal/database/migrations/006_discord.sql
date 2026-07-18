CREATE TABLE discord_guilds (
    guild_id text PRIMARY KEY,
    name text NOT NULL DEFAULT '',
    enabled boolean NOT NULL DEFAULT false,
    community_enabled boolean NOT NULL DEFAULT false,
    application_id text,
    bot_user_id text,
    last_gateway_status text NOT NULL DEFAULT 'disabled',
    last_gateway_error text,
    last_gateway_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE discord_gateway_sessions (
    guild_id text PRIMARY KEY REFERENCES discord_guilds(guild_id) ON DELETE CASCADE,
    session_id text NOT NULL,
    resume_gateway_url text NOT NULL,
    sequence bigint NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE discord_resources (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    guild_id text NOT NULL REFERENCES discord_guilds(guild_id) ON DELETE CASCADE,
    resource_key text NOT NULL,
    discord_id text NOT NULL,
    kind text NOT NULL,
    parent_discord_id text,
    name text NOT NULL,
    managed_marker text NOT NULL,
    status text NOT NULL DEFAULT 'active',
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(guild_id, resource_key),
    UNIQUE(guild_id, discord_id)
);

CREATE TABLE discord_members (
    guild_id text NOT NULL REFERENCES discord_guilds(guild_id) ON DELETE CASCADE,
    discord_user_id text NOT NULL,
    username text NOT NULL DEFAULT '',
    display_name text NOT NULL DEFAULT '',
    is_bot boolean NOT NULL DEFAULT false,
    active boolean NOT NULL DEFAULT true,
    last_synced_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(guild_id, discord_user_id)
);

CREATE TABLE discord_identity_bindings (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    guild_id text NOT NULL REFERENCES discord_guilds(guild_id) ON DELETE CASCADE,
    discord_user_id text NOT NULL,
    github_user_id bigint NOT NULL,
    github_login text NOT NULL,
    version bigint NOT NULL DEFAULT 1,
    status text NOT NULL DEFAULT 'active',
    bound_at timestamptz NOT NULL DEFAULT now(),
    unbound_at timestamptz,
    UNIQUE(guild_id, discord_user_id, version)
);
CREATE UNIQUE INDEX discord_identity_active_user
    ON discord_identity_bindings(guild_id, discord_user_id) WHERE status = 'active';
CREATE UNIQUE INDEX discord_identity_active_github
    ON discord_identity_bindings(guild_id, github_user_id) WHERE status = 'active';

CREATE TABLE discord_oauth_states (
    state_hash char(64) PRIMARY KEY,
    guild_id text NOT NULL REFERENCES discord_guilds(guild_id) ON DELETE CASCADE,
    discord_user_id text NOT NULL,
    code_verifier_ciphertext bytea NOT NULL,
    code_verifier_nonce bytea NOT NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE discord_forums (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    guild_id text NOT NULL REFERENCES discord_guilds(guild_id) ON DELETE CASCADE,
    resource_id uuid NOT NULL UNIQUE REFERENCES discord_resources(id) ON DELETE CASCADE,
    forum_type text NOT NULL CHECK (forum_type IN ('personal', 'repository')),
    owner_discord_user_id text,
    repository_id uuid REFERENCES repositories(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((forum_type = 'personal' AND owner_discord_user_id IS NOT NULL AND repository_id IS NULL)
        OR (forum_type = 'repository' AND owner_discord_user_id IS NULL AND repository_id IS NOT NULL))
);

CREATE TABLE discord_forum_access (
    forum_id uuid NOT NULL REFERENCES discord_forums(id) ON DELETE CASCADE,
    discord_user_id text NOT NULL,
    access_level text NOT NULL CHECK (access_level IN ('readonly', 'operator')),
    granted_by uuid REFERENCES administrators(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(forum_id, discord_user_id)
);

CREATE TABLE discord_conversations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    guild_id text NOT NULL REFERENCES discord_guilds(guild_id) ON DELETE CASCADE,
    forum_id uuid NOT NULL REFERENCES discord_forums(id) ON DELETE CASCADE,
    thread_id text NOT NULL,
    starter_message_id text,
    owner_discord_user_id text NOT NULL,
    repository_id uuid REFERENCES repositories(id),
    agent_profile_id uuid NOT NULL REFERENCES agent_profiles(id),
    title text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'active',
	context_version bigint NOT NULL DEFAULT 1,
    last_activity_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(guild_id, thread_id)
);

CREATE TABLE discord_input_messages (
    message_id text PRIMARY KEY,
    conversation_id uuid NOT NULL REFERENCES discord_conversations(id) ON DELETE CASCADE,
    discord_user_id text NOT NULL,
    participant_id uuid NOT NULL DEFAULT gen_random_uuid(),
    display_name text NOT NULL,
    username text NOT NULL,
    github_binding_id uuid REFERENCES discord_identity_bindings(id),
    github_user_id bigint,
    github_login text,
    binding_version bigint,
    access_snapshot text NOT NULL CHECK (access_snapshot IN ('owner', 'operator')),
    body text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'received',
    received_at timestamptz NOT NULL DEFAULT now(),
    processed_at timestamptz
);

CREATE TABLE discord_attachments (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    message_id text NOT NULL REFERENCES discord_input_messages(message_id) ON DELETE CASCADE,
    discord_attachment_id text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('image', 'file')),
    original_filename text NOT NULL,
    media_type text NOT NULL,
    size_bytes bigint NOT NULL,
	source_url text NOT NULL,
	sha256 char(64),
	relative_path text,
	status text NOT NULL DEFAULT 'pending',
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(message_id, discord_attachment_id)
);

CREATE TABLE discord_turn_contributors (
    conversation_id uuid NOT NULL REFERENCES discord_conversations(id) ON DELETE CASCADE,
    external_turn_id text NOT NULL,
    discord_user_id text NOT NULL,
    first_message_id text NOT NULL REFERENCES discord_input_messages(message_id),
    github_binding_id uuid REFERENCES discord_identity_bindings(id),
    github_user_id bigint,
    github_login text,
    binding_version bigint,
    contributed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(conversation_id, external_turn_id, discord_user_id)
);

CREATE TABLE discord_conversation_workspaces (
    conversation_id uuid PRIMARY KEY REFERENCES discord_conversations(id) ON DELETE CASCADE,
    workspace_type text NOT NULL CHECK (workspace_type IN ('blank', 'worktree')),
    path text NOT NULL UNIQUE,
    branch text,
    base_sha text,
    head_sha text,
    status text NOT NULL DEFAULT 'ready',
    error text,
    last_used_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE discord_conversation_memories (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    conversation_id uuid NOT NULL REFERENCES discord_conversations(id) ON DELETE CASCADE,
    summary text NOT NULL,
    source_thread_id text,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE discord_task_posts (
    work_item_id uuid PRIMARY KEY REFERENCES work_items(id) ON DELETE CASCADE,
    forum_id uuid NOT NULL REFERENCES discord_forums(id) ON DELETE CASCADE,
    thread_id text NOT NULL,
    starter_message_id text NOT NULL,
    last_state text NOT NULL,
    archived boolean NOT NULL DEFAULT false,
    last_projected_at timestamptz,
    UNIQUE(forum_id, thread_id)
);

CREATE TABLE discord_projections (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    guild_id text NOT NULL REFERENCES discord_guilds(guild_id) ON DELETE CASCADE,
    projection_key text NOT NULL,
    resource_id text NOT NULL,
    message_id text,
    desired_version bigint NOT NULL DEFAULT 1,
    applied_version bigint NOT NULL DEFAULT 0,
    desired_payload jsonb NOT NULL,
    last_error text,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    applied_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(guild_id, projection_key)
);

CREATE TABLE discord_initialization_operations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    guild_id text NOT NULL REFERENCES discord_guilds(guild_id) ON DELETE CASCADE,
    mode text NOT NULL CHECK (mode IN ('incremental', 'fresh')),
    status text NOT NULL DEFAULT 'pending',
    requested_by uuid NOT NULL REFERENCES administrators(id),
    preflight jsonb NOT NULL DEFAULT '{}'::jsonb,
    confirmation text,
    error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    finished_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE discord_initialization_steps (
    operation_id uuid NOT NULL REFERENCES discord_initialization_operations(id) ON DELETE CASCADE,
    step_key text NOT NULL,
    ordinal integer NOT NULL,
    status text NOT NULL DEFAULT 'pending',
	attempt_count integer NOT NULL DEFAULT 0,
    request jsonb NOT NULL DEFAULT '{}'::jsonb,
    result jsonb NOT NULL DEFAULT '{}'::jsonb,
    error text,
    started_at timestamptz,
    finished_at timestamptz,
    PRIMARY KEY(operation_id, step_key),
    UNIQUE(operation_id, ordinal)
);

CREATE TABLE discord_inbound_events (
    event_id text PRIMARY KEY,
    guild_id text NOT NULL REFERENCES discord_guilds(guild_id) ON DELETE CASCADE,
    event_type text NOT NULL,
    payload jsonb NOT NULL,
    status text NOT NULL DEFAULT 'received',
    error text,
    received_at timestamptz NOT NULL DEFAULT now(),
    processed_at timestamptz
);

CREATE TABLE integration_outbox (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    integration text NOT NULL,
    operation_key text NOT NULL,
    operation_type text NOT NULL,
    route_key text NOT NULL,
    payload jsonb NOT NULL,
    nonce text,
    status text NOT NULL DEFAULT 'pending',
    attempt_count integer NOT NULL DEFAULT 0,
    max_attempts integer NOT NULL DEFAULT 3,
    available_at timestamptz NOT NULL DEFAULT now(),
    lease_token char(64),
    lease_expires_at timestamptz,
    response jsonb,
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(integration, operation_key)
);
CREATE INDEX integration_outbox_pending
    ON integration_outbox(integration, available_at, created_at) WHERE status IN ('pending', 'retrying');

ALTER TABLE github_app_configs ADD COLUMN client_secret_secret_id uuid REFERENCES encrypted_secrets(id);

ALTER TABLE job_intents
    ALTER COLUMN work_item_id DROP NOT NULL,
    ALTER COLUMN repository_id DROP NOT NULL,
    ADD COLUMN source_type text NOT NULL DEFAULT 'github_work_item',
    ADD COLUMN discord_conversation_id uuid REFERENCES discord_conversations(id) ON DELETE CASCADE,
	ADD COLUMN discord_message_id text REFERENCES discord_input_messages(message_id) ON DELETE SET NULL,
    ADD CONSTRAINT job_intents_source CHECK (
        (source_type = 'github_work_item' AND work_item_id IS NOT NULL AND repository_id IS NOT NULL AND discord_conversation_id IS NULL)
        OR (source_type = 'discord_conversation' AND work_item_id IS NULL AND discord_conversation_id IS NOT NULL)
    );
CREATE INDEX job_intents_discord_conversation ON job_intents(discord_conversation_id, created_at)
    WHERE discord_conversation_id IS NOT NULL;

ALTER TABLE agent_threads
    ALTER COLUMN work_item_id DROP NOT NULL,
    ADD COLUMN source_type text NOT NULL DEFAULT 'github_work_item',
    ADD COLUMN discord_conversation_id uuid REFERENCES discord_conversations(id) ON DELETE CASCADE,
    ADD CONSTRAINT agent_threads_source CHECK (
        (source_type = 'github_work_item' AND work_item_id IS NOT NULL AND discord_conversation_id IS NULL)
        OR (source_type = 'discord_conversation' AND work_item_id IS NULL AND discord_conversation_id IS NOT NULL)
    );
CREATE UNIQUE INDEX agent_threads_discord_context
    ON agent_threads(discord_conversation_id, agent_profile_id, context_version)
    WHERE discord_conversation_id IS NOT NULL;
