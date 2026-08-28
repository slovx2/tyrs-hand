ALTER TABLE workers ALTER COLUMN protocol_version SET DEFAULT 31;
UPDATE workers SET protocol_version = 31 WHERE protocol_version = 30;
