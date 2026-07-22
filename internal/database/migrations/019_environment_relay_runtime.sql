-- 既有容器没有环境 Relay 的宿主目录 bind，需要使用原镜像和持久卷重建一次。
UPDATE discord_development_environments
SET ssh_config_revision = GREATEST(ssh_config_revision, 1),
    daemon_status = 'pending', daemon_error = NULL, updated_at = now()
WHERE status <> 'deleting' AND execution_node_id IS NOT NULL;

INSERT INTO discord_development_operations(environment_id, operation, execution_node_id)
SELECT e.id, 'reconfigure', e.execution_node_id
FROM discord_development_environments e
WHERE e.status <> 'deleting' AND e.execution_node_id IS NOT NULL
AND NOT EXISTS (
    SELECT 1 FROM discord_development_operations o
    WHERE o.environment_id = e.id AND o.operation = 'reconfigure'
      AND o.status IN ('pending','running')
);
