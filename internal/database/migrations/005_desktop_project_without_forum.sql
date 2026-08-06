ALTER TABLE desktop_thread_requests
    ADD COLUMN workspace_project_id uuid;

UPDATE desktop_thread_requests request
SET workspace_project_id = forum.workspace_project_id
FROM discord_forums forum
WHERE forum.id = request.forum_id;

ALTER TABLE desktop_thread_requests
    ALTER COLUMN workspace_project_id SET NOT NULL,
    ADD CONSTRAINT desktop_thread_requests_workspace_project_id_fkey
        FOREIGN KEY (workspace_project_id) REFERENCES workspace_projects(id) ON DELETE RESTRICT;

CREATE INDEX desktop_thread_requests_workspace_project
    ON desktop_thread_requests(workspace_project_id, created_at DESC);
