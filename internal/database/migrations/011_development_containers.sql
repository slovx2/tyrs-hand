ALTER TABLE work_items
    ADD COLUMN head_ref text,
    ADD COLUMN head_repository text,
    ADD COLUMN base_ref text,
    ADD COLUMN html_url text;

ALTER TABLE worktrees
    DROP COLUMN environment_status,
    DROP COLUMN runtime_fingerprint,
    DROP COLUMN dependency_fingerprint,
    DROP COLUMN environment_projects,
    DROP COLUMN environment_diagnostics,
    DROP COLUMN environment_prepared_at;

ALTER TABLE discord_conversations DROP COLUMN workspace_id;
DROP TABLE discord_workspaces;

-- 旧个人 Forum 与按 Post 选仓库模型不再兼容；远端频道由本次 Fresh 初始化清理。
DELETE FROM discord_resources
WHERE id IN (SELECT resource_id FROM discord_forums WHERE forum_type = 'personal');

CREATE TABLE discord_development_environments (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    guild_id text NOT NULL REFERENCES discord_guilds(guild_id) ON DELETE CASCADE,
    owner_discord_user_id text NOT NULL,
    build_repository_id uuid NOT NULL REFERENCES repositories(id) ON DELETE RESTRICT,
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending','building','ready','running','stopped','error','deleting')),
    image_ref text,
    image_id text,
    build_source_sha text,
    container_name text NOT NULL UNIQUE,
    container_id text,
    data_volume_name text NOT NULL UNIQUE,
    home_volume_name text NOT NULL UNIQUE,
    network_name text NOT NULL UNIQUE,
    runtime_user text,
    runtime_uid bigint,
    runtime_gid bigint,
    runtime_home text,
    last_used_at timestamptz NOT NULL DEFAULT now(),
    idle_at timestamptz,
    error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(guild_id, owner_discord_user_id)
);

ALTER TABLE discord_forums
    DROP CONSTRAINT discord_forums_forum_type_check,
    DROP CONSTRAINT discord_forums_check,
    ADD COLUMN development_environment_id uuid
        REFERENCES discord_development_environments(id) ON DELETE CASCADE,
    ADD CONSTRAINT discord_forums_forum_type_check
        CHECK (forum_type IN ('repository','development')),
    ADD CONSTRAINT discord_forums_scope_check CHECK (
        (forum_type = 'repository' AND owner_discord_user_id IS NULL
            AND repository_id IS NOT NULL AND development_environment_id IS NULL)
        OR
        (forum_type = 'development' AND owner_discord_user_id IS NOT NULL
            AND repository_id IS NOT NULL AND development_environment_id IS NOT NULL)
    );

CREATE TABLE discord_forum_workspaces (
    forum_id uuid PRIMARY KEY REFERENCES discord_forums(id) ON DELETE CASCADE,
    environment_id uuid NOT NULL REFERENCES discord_development_environments(id) ON DELETE CASCADE,
    relative_path text NOT NULL,
    branch text NOT NULL,
    base_sha text,
    head_sha text,
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending','cloning','ready','error','deleting')),
    dirty boolean NOT NULL DEFAULT false,
    error text,
    last_used_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(environment_id, relative_path)
);

CREATE TABLE discord_development_operations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    environment_id uuid NOT NULL REFERENCES discord_development_environments(id) ON DELETE CASCADE,
    forum_id uuid REFERENCES discord_forums(id) ON DELETE CASCADE,
    operation text NOT NULL CHECK (operation IN ('provision','clone','rebuild','delete_forum','delete_environment')),
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending','running','completed','failed')),
    requested_by uuid REFERENCES administrators(id),
    attempt_count integer NOT NULL DEFAULT 0,
    lease_token char(64),
    lease_expires_at timestamptz,
    error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    finished_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX discord_development_operations_pending
    ON discord_development_operations(status, created_at)
    WHERE status IN ('pending','running');
