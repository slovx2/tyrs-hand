-- 同一成员可以负责多台 Worker 的 Workspace，保留每台 Worker 的唯一绑定。
ALTER TABLE worker_workspaces DROP CONSTRAINT worker_workspaces_guild_owner_key;

CREATE INDEX worker_workspaces_guild_owner_idx
    ON worker_workspaces (guild_id, owner_discord_user_id);
