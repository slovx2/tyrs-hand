-- 固定升级前客户端 81f6c951d83c0b0e10a97436d9605cba3b3f77ba 的 v12 建表语句，保留真实索引和约束。

PRAGMA auto_vacuum = INCREMENTAL;
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;
CREATE TABLE IF NOT EXISTS connection_profiles (
  profile_id TEXT PRIMARY KEY,
  kind TEXT NOT NULL CHECK(kind='machine'),
  name TEXT NOT NULL,
  active INTEGER NOT NULL DEFAULT 0,
  machine_fingerprint TEXT NOT NULL,
  ssh_host TEXT,
  ssh_port INTEGER,
  ssh_user TEXT,
  ssh_key_ref TEXT,
  ssh_host_fingerprint TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK((ssh_host IS NULL AND ssh_port IS NULL AND ssh_user IS NULL AND ssh_key_ref IS NULL)
    OR (ssh_host IS NOT NULL AND ssh_port IS NOT NULL AND ssh_user IS NOT NULL
      AND ssh_key_ref IS NOT NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS connection_profiles_one_active
  ON connection_profiles(active) WHERE active=1;
CREATE TABLE IF NOT EXISTS ssh_projects (
  profile_id TEXT NOT NULL,
  id TEXT NOT NULL,
  remote_path TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY(profile_id,id),
  UNIQUE(profile_id,remote_path),
  FOREIGN KEY(profile_id) REFERENCES connection_profiles(profile_id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS projects (
  profile_id TEXT NOT NULL,
  id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  name TEXT NOT NULL,
  relative_path TEXT NOT NULL,
  payload TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY(profile_id,id),
  FOREIGN KEY(profile_id) REFERENCES connection_profiles(profile_id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS threads (
  profile_id TEXT NOT NULL,
  id TEXT NOT NULL,
  archived INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL,
  payload TEXT NOT NULL,
  PRIMARY KEY(profile_id,id),
  FOREIGN KEY(profile_id) REFERENCES connection_profiles(profile_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS threads_recency ON threads(profile_id,archived,updated_at DESC);
CREATE TABLE IF NOT EXISTS thread_reads (
  profile_id TEXT NOT NULL,
  thread_id TEXT NOT NULL,
  has_unread INTEGER NOT NULL DEFAULT 0 CHECK(has_unread IN (0,1)),
  updated_at TEXT NOT NULL,
  PRIMARY KEY(profile_id,thread_id),
  FOREIGN KEY(profile_id) REFERENCES connection_profiles(profile_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS thread_reads_unread
  ON thread_reads(profile_id,has_unread,updated_at DESC);
CREATE TABLE IF NOT EXISTS drafts (
  profile_id TEXT NOT NULL,
  scope TEXT NOT NULL,
  text TEXT NOT NULL,
  settings TEXT,
  attachment_ids TEXT NOT NULL DEFAULT '[]',
  updated_at TEXT NOT NULL,
  PRIMARY KEY(profile_id,scope),
  FOREIGN KEY(profile_id) REFERENCES connection_profiles(profile_id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS pending_submissions (
  profile_id TEXT NOT NULL,
  client_message_id TEXT NOT NULL,
  thread_id TEXT,
  project_id TEXT,
  payload TEXT NOT NULL,
  state TEXT NOT NULL CHECK(state IN ('prepared','unknown')),
  error TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY(profile_id,client_message_id),
  FOREIGN KEY(profile_id) REFERENCES connection_profiles(profile_id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS outbox (
  profile_id TEXT NOT NULL,
  client_message_id TEXT NOT NULL,
  kind TEXT NOT NULL CHECK(kind IN ('create_task','submit_message')),
  project_id TEXT NOT NULL,
  thread_id TEXT,
  payload TEXT NOT NULL,
  state TEXT NOT NULL CHECK(state IN ('pending','processing','failed')),
  attempt_count INTEGER NOT NULL DEFAULT 0,
  error TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY(profile_id,client_message_id),
  FOREIGN KEY(profile_id) REFERENCES connection_profiles(profile_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS outbox_profile_created
  ON outbox(profile_id,created_at);
CREATE TABLE IF NOT EXISTS pending_message_previews (
  profile_id TEXT NOT NULL,
  client_message_id TEXT NOT NULL,
  thread_id TEXT,
  project_id TEXT NOT NULL,
  text TEXT NOT NULL,
  attachments TEXT NOT NULL DEFAULT '[]',
  created_at TEXT NOT NULL,
  PRIMARY KEY(profile_id,client_message_id),
  FOREIGN KEY(profile_id) REFERENCES connection_profiles(profile_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS pending_message_previews_profile_created
  ON pending_message_previews(profile_id,created_at);
CREATE TABLE IF NOT EXISTS app_settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS connection_profiles_machine_fingerprint
  ON connection_profiles(machine_fingerprint);
CREATE TABLE IF NOT EXISTS control_machine_links (
  profile_id TEXT NOT NULL,
  server_id TEXT NOT NULL,
  base_url TEXT NOT NULL,
  worker_id TEXT NOT NULL,
  worker_name TEXT NOT NULL,
  device_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY(server_id,worker_id),
  UNIQUE(profile_id,server_id),
  FOREIGN KEY(profile_id) REFERENCES connection_profiles(profile_id) ON DELETE CASCADE
);

PRAGMA user_version=12;
