ALTER TABLE live_conversations
	ADD COLUMN IF NOT EXISTS worker_id uuid REFERENCES workers(id) ON DELETE RESTRICT,
	ADD COLUMN IF NOT EXISTS project_id uuid REFERENCES workspace_projects(id) ON DELETE RESTRICT,
	ADD COLUMN IF NOT EXISTS workspace_session_id uuid REFERENCES workspace_sessions(id) ON DELETE SET NULL,
	ADD COLUMN IF NOT EXISTS previous_workspace_session_id uuid REFERENCES workspace_sessions(id) ON DELETE SET NULL,
	ADD COLUMN IF NOT EXISTS last_delegation_id text NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS live_conversations_worker_idx ON live_conversations(worker_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS live_conversations_session_idx ON live_conversations(workspace_session_id)
	WHERE workspace_session_id IS NOT NULL;
