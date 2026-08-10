ALTER TABLE workers ALTER COLUMN protocol_version SET DEFAULT 27;

UPDATE workers SET protocol_version = 27 WHERE protocol_version = 26;
