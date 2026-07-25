CREATE TABLE projects (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    guild_id text NOT NULL REFERENCES discord_guilds(guild_id) ON DELETE RESTRICT,
    owner_discord_user_id text NOT NULL,
    forum_id uuid NOT NULL UNIQUE DEFAULT gen_random_uuid(),
    name text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 80),
    status text NOT NULL DEFAULT 'provisioning'
        CHECK (status IN ('provisioning','active','disabled','error')),
    error text,
    requested_by uuid NOT NULL REFERENCES administrators(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX projects_guild_name
    ON projects(guild_id, lower(btrim(name)));

ALTER TABLE discord_forums
    DROP CONSTRAINT discord_forums_scope_check,
    ADD COLUMN project_id uuid UNIQUE REFERENCES projects(id) ON DELETE RESTRICT,
    ADD CONSTRAINT discord_forums_scope_check CHECK (
        (forum_type = 'repository' AND owner_discord_user_id IS NULL
            AND repository_id IS NOT NULL AND project_id IS NULL
            AND development_environment_id IS NULL)
        OR
        (forum_type = 'development' AND owner_discord_user_id IS NOT NULL
            AND development_environment_id IS NOT NULL
            AND ((repository_id IS NOT NULL AND project_id IS NULL)
                OR (repository_id IS NULL AND project_id IS NOT NULL)))
    );

ALTER TABLE discord_conversations
    ADD COLUMN project_id uuid REFERENCES projects(id) ON DELETE RESTRICT,
    ADD CONSTRAINT discord_conversations_scope_check CHECK (
        (repository_id IS NOT NULL AND project_id IS NULL)
        OR (repository_id IS NULL AND project_id IS NOT NULL)
    );

ALTER TABLE codex_thread_controls
    DROP CONSTRAINT codex_thread_controls_source_check,
    ADD COLUMN project_id uuid REFERENCES projects(id) ON DELETE RESTRICT,
    ADD CONSTRAINT codex_thread_controls_source_check CHECK (
        (source_type = 'github_work_item' AND work_item_id IS NOT NULL
            AND discord_conversation_id IS NULL AND repository_id IS NOT NULL
            AND project_id IS NULL)
        OR
        (source_type = 'discord_conversation' AND work_item_id IS NULL
            AND discord_conversation_id IS NOT NULL
            AND ((repository_id IS NOT NULL AND project_id IS NULL)
                OR (repository_id IS NULL AND project_id IS NOT NULL)))
        OR
        (source_type = 'desktop_thread' AND work_item_id IS NULL
            AND development_environment_id IS NOT NULL
            AND ((repository_id IS NOT NULL AND project_id IS NULL)
                OR (repository_id IS NULL AND project_id IS NOT NULL)))
    );

ALTER TABLE codex_turn_intents
    DROP CONSTRAINT codex_turn_intents_source_check,
    ADD COLUMN project_id uuid REFERENCES projects(id) ON DELETE RESTRICT;

-- 早期 Desktop 首发 Intent 尚未直接保存工作区范围，迁移时从所属 Control 补齐。
UPDATE codex_turn_intents intent
SET repository_id = control.repository_id,
    project_id = control.project_id
FROM codex_thread_controls control
WHERE intent.control_id = control.id
    AND intent.input_surface = 'desktop'
    AND intent.repository_id IS NULL
    AND intent.project_id IS NULL;

ALTER TABLE codex_turn_intents
    ADD CONSTRAINT codex_turn_intents_source_check CHECK (
        (source_type = 'github_work_item' AND work_item_id IS NOT NULL
            AND discord_conversation_id IS NULL AND repository_id IS NOT NULL
            AND project_id IS NULL)
        OR
        (source_type = 'discord_conversation' AND work_item_id IS NULL
            AND (discord_conversation_id IS NOT NULL OR input_surface = 'desktop')
            AND ((repository_id IS NOT NULL AND project_id IS NULL)
                OR (repository_id IS NULL AND project_id IS NOT NULL)))
    );

ALTER TABLE discord_initialization_operations
    ADD COLUMN project_id uuid REFERENCES projects(id) ON DELETE RESTRICT;
CREATE INDEX discord_initialization_operations_project
    ON discord_initialization_operations(project_id, created_at DESC)
    WHERE project_id IS NOT NULL;

ALTER TABLE execution_nodes ALTER COLUMN protocol_version SET DEFAULT 11;
UPDATE execution_nodes SET protocol_version = 11, status = 'pending', last_error = NULL;
