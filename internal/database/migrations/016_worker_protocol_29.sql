ALTER TABLE workers ALTER COLUMN protocol_version SET DEFAULT 29;
UPDATE workers SET protocol_version = 29 WHERE protocol_version = 28;
