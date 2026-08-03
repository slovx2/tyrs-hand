ALTER TABLE execution_nodes ALTER COLUMN protocol_version SET DEFAULT 20;

UPDATE execution_nodes
SET protocol_version = 20, status = 'pending', last_error = NULL, updated_at = now();
