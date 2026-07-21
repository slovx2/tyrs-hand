ALTER TABLE discord_development_operations
    DROP CONSTRAINT discord_development_operations_operation_check,
    ADD CONSTRAINT discord_development_operations_operation_check
        CHECK (operation IN (
            'provision','clone','start','stop','rebuild','delete_forum','delete_environment'
        )),
    ADD COLUMN execution_node_id uuid REFERENCES execution_nodes(id) ON DELETE RESTRICT,
    ADD COLUMN lease_epoch bigint NOT NULL DEFAULT 0,
    ADD COLUMN worker_id text,
    ADD COLUMN terminal_key text;

ALTER TABLE discord_development_operations
    DROP CONSTRAINT discord_development_operations_environment_id_fkey,
    DROP CONSTRAINT discord_development_operations_forum_id_fkey,
    ALTER COLUMN environment_id DROP NOT NULL,
    ADD CONSTRAINT discord_development_operations_environment_id_fkey
        FOREIGN KEY (environment_id) REFERENCES discord_development_environments(id) ON DELETE SET NULL,
    ADD CONSTRAINT discord_development_operations_forum_id_fkey
        FOREIGN KEY (forum_id) REFERENCES discord_forums(id) ON DELETE SET NULL;

UPDATE discord_development_operations o
SET execution_node_id = e.execution_node_id
FROM discord_development_environments e
WHERE e.id = o.environment_id AND o.execution_node_id IS NULL;

CREATE INDEX discord_development_operations_node_claim
    ON discord_development_operations(execution_node_id, status, created_at)
    WHERE status IN ('pending','running');

CREATE UNIQUE INDEX discord_development_operations_terminal_key
    ON discord_development_operations(id, terminal_key)
    WHERE terminal_key IS NOT NULL;
