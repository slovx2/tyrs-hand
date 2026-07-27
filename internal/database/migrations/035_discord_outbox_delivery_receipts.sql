ALTER TABLE integration_outbox
    ADD COLUMN request_revision bigint NOT NULL DEFAULT 1
        CHECK (request_revision > 0),
    ADD COLUMN inflight_revision bigint CHECK (inflight_revision > 0),
    ADD COLUMN inflight_operation_type text,
    ADD COLUMN inflight_route_key text,
    ADD COLUMN inflight_payload jsonb,
    ADD COLUMN inflight_nonce text,
    ADD COLUMN delivered_at timestamptz,
    ADD COLUMN apply_attempt_count integer NOT NULL DEFAULT 0
        CHECK (apply_attempt_count >= 0);

-- 旧进程可能在 Discord 已成功后、数据库 Complete 前退出。升级时保留当时的
-- 请求快照并转为人工可对账状态，绝不能把非幂等的 Forum Post 自动重发。
UPDATE integration_outbox
SET inflight_revision = request_revision,
    inflight_operation_type = operation_type,
    inflight_route_key = route_key,
    inflight_payload = payload,
    inflight_nonce = nonce,
    status = 'ambiguous',
    lease_token = NULL,
    lease_expires_at = NULL,
    last_error = '升级时发现投递租约未完成；为避免重复外部写入已停止自动重发',
    updated_at = now()
WHERE status = 'sending';

ALTER TABLE integration_outbox
    ADD CONSTRAINT integration_outbox_inflight_check CHECK (
        (status IN ('sending','applying','ambiguous')
            AND inflight_revision IS NOT NULL
            AND inflight_operation_type IS NOT NULL
            AND inflight_route_key IS NOT NULL
            AND inflight_payload IS NOT NULL)
        OR
        (status NOT IN ('sending','applying','ambiguous')
            AND inflight_revision IS NULL
            AND inflight_operation_type IS NULL
            AND inflight_route_key IS NULL
            AND inflight_payload IS NULL
            AND inflight_nonce IS NULL)
    );

DROP INDEX integration_outbox_pending;
CREATE INDEX integration_outbox_dispatchable
    ON integration_outbox(integration, available_at, created_at)
    WHERE status IN ('pending','retrying','applying');
CREATE INDEX integration_outbox_expired_delivery
    ON integration_outbox(integration, lease_expires_at)
    WHERE status = 'sending';
