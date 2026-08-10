ALTER TABLE workers ALTER COLUMN protocol_version SET DEFAULT 26;

UPDATE workers SET protocol_version = 26 WHERE protocol_version = 25;
