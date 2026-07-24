ALTER TABLE execution_nodes ALTER COLUMN protocol_version SET DEFAULT 9;
UPDATE execution_nodes SET protocol_version = 9, status = 'pending', last_error = NULL;

ALTER TABLE discord_development_operations
    DROP CONSTRAINT discord_development_operations_operation_check;

UPDATE discord_development_operations
SET operation = 'rebase'
WHERE operation = 'rebuild';

ALTER TABLE discord_development_operations
    ADD CONSTRAINT discord_development_operations_operation_check CHECK (
        operation IN ('provision','clone','rebase','reconfigure','delete_forum','delete_environment')
    );

ALTER TABLE discord_development_environments
    DROP COLUMN build_repository_id,
    DROP COLUMN build_source_sha,
    ADD COLUMN codex_version text,
    ADD COLUMN codex_user_override boolean NOT NULL DEFAULT false;

UPDATE discord_development_environments
SET status = 'error',
    error = '开发环境需要 Rebase 到官方开发镜像后才能继续使用',
    updated_at = now()
WHERE status <> 'deleting';
