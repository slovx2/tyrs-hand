DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM discord_development_operations
        WHERE status IN ('pending','running')
    ) THEN
        RAISE EXCEPTION '存在未完成的开发环境操作，无法迁移';
    END IF;
    IF EXISTS (
        SELECT 1
        FROM discord_initialization_operations
        WHERE status IN ('pending','running')
    ) THEN
        RAISE EXCEPTION '存在未完成的 Discord 初始化操作，无法迁移';
    END IF;
    IF EXISTS (
        SELECT 1
        FROM codex_thread_controls
        WHERE development_environment_id IS NOT NULL
          AND status IN ('dispatching','active','stopping','reconciling')
    ) THEN
        RAISE EXCEPTION '存在运行中的开发会话，无法迁移';
    END IF;
    IF EXISTS (
        SELECT 1
        FROM discord_forums forum
        LEFT JOIN discord_forum_workspaces workspace ON workspace.forum_id = forum.id
        WHERE forum.forum_type = 'development'
          AND workspace.forum_id IS NULL
    ) THEN
        RAISE EXCEPTION '开发 Forum 缺少工作区记录，无法迁移';
    END IF;
END $$;

ALTER TABLE projects RENAME TO development_projects;

ALTER TABLE discord_development_environments
    ADD COLUMN projects_scanned_at timestamptz,
    ADD COLUMN project_scan_error text;

ALTER TABLE development_projects
    ADD COLUMN environment_id uuid
        REFERENCES discord_development_environments(id) ON DELETE CASCADE,
    ADD COLUMN relative_path text,
    ADD COLUMN desired_relative_path text,
    ADD COLUMN project_kind text NOT NULL DEFAULT 'git'
        CHECK (project_kind IN ('directory','git')),
    ADD COLUMN availability_status text NOT NULL DEFAULT 'available'
        CHECK (availability_status IN ('available','missing')),
    ADD COLUMN remote_url text,
    ADD COLUMN branch text,
    ADD COLUMN head_sha text,
    ADD COLUMN dirty boolean NOT NULL DEFAULT false,
    ADD COLUMN last_seen_at timestamptz,
    ADD COLUMN scan_error text;

-- 此列仅属于已废弃的页面创建流程，迁移历史仓库 Forum 时不应依赖管理员记录。
ALTER TABLE development_projects
    ALTER COLUMN requested_by DROP NOT NULL;

UPDATE development_projects project
SET environment_id = forum.development_environment_id,
    relative_path = workspace.relative_path,
    branch = NULLIF(workspace.branch, ''),
    head_sha = workspace.head_sha,
    dirty = workspace.dirty,
    availability_status = CASE
        WHEN workspace.status = 'ready' THEN 'available'
        ELSE 'missing'
    END,
    last_seen_at = workspace.last_used_at,
    scan_error = workspace.error
FROM discord_forums forum
JOIN discord_forum_workspaces workspace ON workspace.forum_id = forum.id
WHERE forum.project_id = project.id;

CREATE TEMP TABLE development_project_migration_map (
    forum_id uuid PRIMARY KEY,
    legacy_repository_id uuid,
    development_project_id uuid NOT NULL UNIQUE
) ON COMMIT DROP;

INSERT INTO development_project_migration_map
    (forum_id, legacy_repository_id, development_project_id)
SELECT forum.id, forum.repository_id, gen_random_uuid()
FROM discord_forums forum
WHERE forum.forum_type = 'development'
    AND forum.repository_id IS NOT NULL;

INSERT INTO development_projects (
    id, guild_id, owner_discord_user_id, forum_id, name, status, requested_by,
    environment_id, relative_path, project_kind, availability_status,
    branch, head_sha, dirty, remote_url, last_seen_at, scan_error
)
SELECT migration.development_project_id,
    forum.guild_id,
    forum.owner_discord_user_id,
    forum.id,
    repository.name,
    CASE WHEN workspace.status = 'ready' THEN 'active' ELSE 'error' END,
    (SELECT administrator.id FROM administrators administrator
        ORDER BY administrator.created_at LIMIT 1),
    forum.development_environment_id,
    workspace.relative_path,
    'git',
    CASE WHEN workspace.status = 'ready' THEN 'available' ELSE 'missing' END,
    NULLIF(workspace.branch, ''),
    workspace.head_sha,
    workspace.dirty,
    regexp_replace(repository.clone_url, '(https?://)[^/@]+@', '\1', 'i'),
    workspace.last_used_at,
    workspace.error
FROM development_project_migration_map migration
JOIN discord_forums forum ON forum.id = migration.forum_id
JOIN repositories repository ON repository.id = migration.legacy_repository_id
JOIN discord_forum_workspaces workspace ON workspace.forum_id = forum.id;

UPDATE discord_forums forum
SET project_id = migration.development_project_id,
    repository_id = NULL
FROM development_project_migration_map migration
WHERE forum.id = migration.forum_id;

UPDATE discord_conversations conversation
SET project_id = forum.project_id,
    repository_id = NULL
FROM discord_forums forum
WHERE conversation.forum_id = forum.id
    AND forum.forum_type = 'development';

UPDATE codex_thread_controls control
SET project_id = conversation.project_id,
    repository_id = NULL
FROM discord_conversations conversation
WHERE control.discord_conversation_id = conversation.id
    AND control.source_type = 'discord_conversation';

UPDATE codex_thread_controls control
SET project_id = migration.development_project_id,
    repository_id = NULL
FROM development_project_migration_map migration
JOIN discord_forums forum ON forum.id = migration.forum_id
WHERE control.source_type = 'desktop_thread'
    AND control.development_environment_id = forum.development_environment_id
    AND control.repository_id = migration.legacy_repository_id;

UPDATE codex_turn_intents intent
SET project_id = control.project_id,
    repository_id = NULL
FROM codex_thread_controls control
WHERE intent.control_id = control.id
    AND intent.source_type = 'discord_conversation';

UPDATE discord_development_environments environment
SET projects_scanned_at = scan.last_scanned_at
FROM (
    SELECT environment_id, max(last_seen_at) AS last_scanned_at
    FROM development_projects
    GROUP BY environment_id
) scan
WHERE scan.environment_id = environment.id;

ALTER TABLE development_projects
    DROP CONSTRAINT projects_status_check,
    DROP COLUMN guild_id,
    DROP COLUMN owner_discord_user_id,
    DROP COLUMN forum_id,
    DROP COLUMN status,
    DROP COLUMN error,
    DROP COLUMN requested_by,
    ALTER COLUMN environment_id SET NOT NULL,
    ALTER COLUMN relative_path SET NOT NULL,
    ALTER COLUMN last_seen_at SET DEFAULT now(),
    ALTER COLUMN last_seen_at SET NOT NULL;

CREATE UNIQUE INDEX development_projects_environment_path
    ON development_projects(environment_id, relative_path);
CREATE INDEX development_projects_environment_status
    ON development_projects(environment_id, availability_status, lower(name));

UPDATE development_projects
SET desired_relative_path = 'workspaces/' ||
    trim(both '-' FROM regexp_replace(lower(name), '[^a-z0-9._-]+', '-', 'g'))
WHERE relative_path LIKE 'workspaces/projects/%';

ALTER TABLE discord_forums
    DROP CONSTRAINT discord_forums_scope_check,
    DROP CONSTRAINT discord_forums_project_id_key;
ALTER TABLE discord_forums
    RENAME COLUMN project_id TO development_project_id;

ALTER TABLE discord_forums
    ADD COLUMN binding_status text NOT NULL DEFAULT 'active'
        CHECK (binding_status IN ('active','inactive')),
    ADD CONSTRAINT discord_forums_scope_check CHECK (
        (forum_type = 'repository'
            AND owner_discord_user_id IS NULL
            AND repository_id IS NOT NULL
            AND development_project_id IS NULL
            AND development_environment_id IS NULL)
        OR
        (forum_type = 'development'
            AND owner_discord_user_id IS NOT NULL
            AND repository_id IS NULL
            AND development_project_id IS NOT NULL
            AND development_environment_id IS NOT NULL)
    );
CREATE UNIQUE INDEX discord_forums_active_development_project
    ON discord_forums(development_project_id)
    WHERE forum_type = 'development' AND binding_status = 'active';

ALTER TABLE discord_conversations
    DROP CONSTRAINT discord_conversations_scope_check;
ALTER TABLE discord_conversations
    RENAME COLUMN project_id TO development_project_id;
ALTER TABLE discord_conversations
    ADD CONSTRAINT discord_conversations_scope_check CHECK (
        (repository_id IS NOT NULL AND development_project_id IS NULL)
        OR (repository_id IS NULL AND development_project_id IS NOT NULL)
    );

ALTER TABLE codex_thread_controls
    DROP CONSTRAINT codex_thread_controls_source_check;
ALTER TABLE codex_thread_controls
    RENAME COLUMN project_id TO development_project_id;
ALTER TABLE codex_thread_controls
    ADD CONSTRAINT codex_thread_controls_source_check CHECK (
        (source_type = 'github_work_item'
            AND work_item_id IS NOT NULL
            AND discord_conversation_id IS NULL
            AND repository_id IS NOT NULL
            AND development_project_id IS NULL)
        OR
        (source_type = 'discord_conversation'
            AND work_item_id IS NULL
            AND discord_conversation_id IS NOT NULL
            AND repository_id IS NULL
            AND development_project_id IS NOT NULL)
        OR
        (source_type = 'desktop_thread'
            AND work_item_id IS NULL
            AND development_environment_id IS NOT NULL
            AND repository_id IS NULL
            AND development_project_id IS NOT NULL)
    );

ALTER TABLE codex_turn_intents
    DROP CONSTRAINT codex_turn_intents_source_check;
ALTER TABLE codex_turn_intents
    RENAME COLUMN project_id TO development_project_id;
ALTER TABLE codex_turn_intents
    ADD CONSTRAINT codex_turn_intents_source_check CHECK (
        (source_type = 'github_work_item'
            AND work_item_id IS NOT NULL
            AND discord_conversation_id IS NULL
            AND repository_id IS NOT NULL
            AND development_project_id IS NULL)
        OR
        (source_type = 'discord_conversation'
            AND work_item_id IS NULL
            AND (discord_conversation_id IS NOT NULL OR input_surface = 'desktop')
            AND repository_id IS NULL
            AND development_project_id IS NOT NULL)
    );

DROP INDEX discord_initialization_operations_project;
ALTER TABLE discord_initialization_operations
    RENAME COLUMN project_id TO development_project_id;
CREATE INDEX discord_initialization_operations_development_project
    ON discord_initialization_operations(development_project_id, created_at DESC)
    WHERE development_project_id IS NOT NULL;

ALTER TABLE discord_development_operations
    DROP CONSTRAINT discord_development_operations_operation_check,
    ADD COLUMN development_project_id uuid
        REFERENCES development_projects(id) ON DELETE SET NULL;

ALTER TABLE discord_development_operations
    ADD CONSTRAINT discord_development_operations_operation_check CHECK (
        operation IN (
            'provision_environment','relocate_project',
            'rebase','reconfigure',
            'provision','clone','delete_forum','delete_environment'
        )
    );

INSERT INTO discord_development_operations (
    environment_id, development_project_id, operation, execution_node_id
)
SELECT project.environment_id, project.id, 'relocate_project', environment.execution_node_id
FROM development_projects project
JOIN discord_development_environments environment ON environment.id = project.environment_id
WHERE project.desired_relative_path IS NOT NULL
    AND environment.execution_node_id IS NOT NULL;

DROP TABLE discord_forum_workspaces;

ALTER TABLE execution_nodes ALTER COLUMN protocol_version SET DEFAULT 13;
UPDATE execution_nodes
SET protocol_version = 13, status = 'pending', last_error = NULL;
