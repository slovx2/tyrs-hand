-- 固定运行时身份需要新版 Worker 与客户端协调升级，不推断旧入口引擎。
ALTER TABLE workers ALTER COLUMN protocol_version SET DEFAULT 33;
UPDATE workers SET protocol_version = 33 WHERE protocol_version = 32;
