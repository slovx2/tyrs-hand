ALTER TABLE workers ALTER COLUMN protocol_version SET DEFAULT 24;

UPDATE workers SET protocol_version = 24 WHERE protocol_version = 23;
