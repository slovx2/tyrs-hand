ALTER TABLE execution_nodes ALTER COLUMN protocol_version SET DEFAULT 22;
UPDATE execution_nodes
SET protocol_version = 22, status = 'pending', last_error = NULL, updated_at = now();

-- 宿主 Worker 只把 Environment 作为 Worker、项目和 Forum 的逻辑绑定。
-- 旧容器资源字段不再参与新记录创建，遗留 Operation 也不得再被 Worker 领取。
ALTER TABLE discord_development_environments
    ALTER COLUMN container_name DROP NOT NULL,
    ALTER COLUMN data_volume_name DROP NOT NULL,
    ALTER COLUMN home_volume_name DROP NOT NULL,
    ALTER COLUMN network_name DROP NOT NULL;

UPDATE discord_development_operations
SET status = 'failed',
    error = '宿主 Worker 架构不再执行开发容器 Operation',
    lease_token = NULL,
    lease_expires_at = NULL,
    finished_at = now(),
    updated_at = now()
WHERE status IN ('pending', 'running');
