-- 同一 Worker 的两个引擎分别持有入口身份、健康状态和模型目录。
CREATE TABLE worker_runtimes (
    worker_id uuid NOT NULL REFERENCES workers(id) ON DELETE CASCADE,
    engine text NOT NULL CHECK (engine IN ('codex','claude-code')),
    enabled boolean NOT NULL,
    status text NOT NULL CHECK (status IN ('running','unavailable','stopped','disabled','offline','incompatible')),
    ssh_listen_address text NOT NULL,
    ssh_host_key_fingerprint text,
    protocol_version text NOT NULL,
    build jsonb NOT NULL DEFAULT '{}',
    capabilities jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(capabilities) = 'array'),
    model_catalog jsonb,
    release_ready boolean NOT NULL DEFAULT false,
    heartbeat_at timestamptz,
    PRIMARY KEY(worker_id,engine),
    CONSTRAINT worker_runtimes_host_key_unique UNIQUE(ssh_host_key_fingerprint)
);

-- 旧数据仅回填为 Codex，不推测不存在的 Claude 配置。
INSERT INTO worker_runtimes(worker_id,engine,enabled,status,ssh_listen_address,
    ssh_host_key_fingerprint,protocol_version,model_catalog,heartbeat_at)
SELECT id,'codex',enabled,'offline',COALESCE(metadata->'ssh'->>'listenAddress',':2222'),
    ssh_host_key_fingerprint,'',metadata->'modelCatalog',heartbeat_at FROM workers;
