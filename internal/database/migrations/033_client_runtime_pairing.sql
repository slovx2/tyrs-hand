-- 老授权一次性归属 Codex；新授权必须明确选择运行时。
ALTER TABLE client_device_workers ADD COLUMN engine text NOT NULL DEFAULT 'codex'
    CHECK (engine IN ('codex','claude-code'));
ALTER TABLE client_device_pairings ADD COLUMN engine text NOT NULL DEFAULT 'codex'
    CHECK (engine IN ('codex','claude-code'));
ALTER TABLE client_device_workers ALTER COLUMN engine DROP DEFAULT;
ALTER TABLE client_device_pairings ALTER COLUMN engine DROP DEFAULT;
ALTER TABLE client_device_workers DROP CONSTRAINT client_device_workers_pkey;
ALTER TABLE client_device_workers ADD PRIMARY KEY(device_id,worker_id,engine);

CREATE FUNCTION prevent_client_runtime_identity_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.worker_id IS DISTINCT FROM OLD.worker_id OR NEW.engine IS DISTINCT FROM OLD.engine THEN
        RAISE EXCEPTION 'client runtime identity is immutable';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER client_pairing_runtime_immutable BEFORE UPDATE ON client_device_pairings
    FOR EACH ROW EXECUTE FUNCTION prevent_client_runtime_identity_change();
CREATE TRIGGER client_binding_runtime_immutable BEFORE UPDATE ON client_device_workers
    FOR EACH ROW EXECUTE FUNCTION prevent_client_runtime_identity_change();
