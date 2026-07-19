DROP TABLE discord_conversation_workspaces;

CREATE TABLE discord_workspaces (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    guild_id text NOT NULL REFERENCES discord_guilds(guild_id) ON DELETE CASCADE,
    owner_discord_user_id text NOT NULL,
    repository_id uuid NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    name text NOT NULL,
    repo_cache_id uuid REFERENCES repo_caches(id) ON DELETE SET NULL,
    path text NOT NULL UNIQUE,
    branch text NOT NULL,
    base_sha text,
    head_sha text,
    status text NOT NULL DEFAULT 'initializing',
    dirty boolean NOT NULL DEFAULT false,
    environment_status text NOT NULL DEFAULT 'pending',
    runtime_fingerprint char(64),
    dependency_fingerprint char(64),
    environment_projects jsonb NOT NULL DEFAULT '[]'::jsonb,
    environment_diagnostics jsonb NOT NULL DEFAULT '[]'::jsonb,
    environment_prepared_at timestamptz,
    error text,
    last_used_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(guild_id, owner_discord_user_id, repository_id, name)
);

ALTER TABLE discord_conversations
    ADD COLUMN workspace_id uuid REFERENCES discord_workspaces(id) ON DELETE SET NULL;

CREATE INDEX idx_discord_conversations_workspace ON discord_conversations(workspace_id);

ALTER TABLE worktrees
    ADD COLUMN environment_status text NOT NULL DEFAULT 'pending',
    ADD COLUMN runtime_fingerprint char(64),
    ADD COLUMN dependency_fingerprint char(64),
    ADD COLUMN environment_projects jsonb NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN environment_diagnostics jsonb NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN environment_prepared_at timestamptz;
