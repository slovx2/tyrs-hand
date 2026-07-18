ALTER TABLE agent_threads
    ADD COLUMN provider_signature text NOT NULL DEFAULT '';

CREATE INDEX idx_agent_threads_expiry
    ON agent_threads(status, expires_at)
    WHERE expires_at IS NOT NULL;
