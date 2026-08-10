ALTER TABLE workers ALTER COLUMN protocol_version SET DEFAULT 25;

UPDATE workers SET protocol_version = 25 WHERE protocol_version = 24;
