ALTER TABLE workers ALTER COLUMN protocol_version SET DEFAULT 32;
UPDATE workers SET protocol_version = 32 WHERE protocol_version = 31;

-- 第一阶段保留旧列以便回滚，但 Codex Run 不再使用时间租约或能力令牌。
ALTER TABLE codex_turn_runs ALTER COLUMN lease_owner DROP NOT NULL;
ALTER TABLE codex_turn_runs ALTER COLUMN lease_epoch DROP NOT NULL;
ALTER TABLE codex_turn_runs ALTER COLUMN capability_hash DROP NOT NULL;
