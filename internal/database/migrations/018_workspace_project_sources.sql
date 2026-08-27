ALTER TABLE workspace_projects
    ADD COLUMN IF NOT EXISTS project_source text NOT NULL DEFAULT 'workspace_child',
    ADD COLUMN IF NOT EXISTS host_path text;

ALTER TABLE workspace_projects
    DROP CONSTRAINT IF EXISTS workspace_projects_project_source_check;
ALTER TABLE workspace_projects
    ADD CONSTRAINT workspace_projects_project_source_check
    CHECK (project_source = ANY (ARRAY['workspace_root'::text, 'workspace_child'::text, 'codex_registered'::text]));

CREATE INDEX IF NOT EXISTS workspace_projects_source ON workspace_projects (workspace_id, project_source);

ALTER TABLE workers ALTER COLUMN protocol_version SET DEFAULT 30;
UPDATE workers SET protocol_version = 30 WHERE protocol_version = 29;
