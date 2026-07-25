ALTER TABLE execution_nodes ALTER COLUMN protocol_version SET DEFAULT 10;
UPDATE execution_nodes SET protocol_version = 10, status = 'pending', last_error = NULL;

INSERT INTO discord_development_operations
    (environment_id, forum_id, operation, execution_node_id)
SELECT fw.environment_id, fw.forum_id, 'provision', e.execution_node_id
FROM discord_forum_workspaces fw
JOIN discord_development_environments e ON e.id = fw.environment_id
WHERE fw.status = 'pending'
  AND e.status <> 'deleting'
  AND e.execution_node_id IS NOT NULL
  AND NOT EXISTS (
      SELECT 1 FROM discord_development_operations operation
      WHERE operation.forum_id = fw.forum_id
        AND operation.operation = 'provision'
  );
