ALTER TABLE execution_nodes ALTER COLUMN protocol_version SET DEFAULT 7;
UPDATE execution_nodes SET protocol_version = 7, status = 'pending', last_error = NULL;
