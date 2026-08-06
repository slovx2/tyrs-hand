import * as SQLite from "expo-sqlite";

let databasePromise: Promise<SQLite.SQLiteDatabase> | null = null;
let writeQueue: Promise<void> = Promise.resolve();

const schema = `
PRAGMA auto_vacuum = INCREMENTAL;
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;
CREATE TABLE IF NOT EXISTS connections (
  server_id TEXT PRIMARY KEY,
  base_url TEXT NOT NULL,
  name TEXT NOT NULL,
  device_id TEXT NOT NULL,
  active INTEGER NOT NULL DEFAULT 0,
  session_reads_initialized INTEGER NOT NULL DEFAULT 0,
  bootstrap_payload TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS projects (
  server_id TEXT NOT NULL,
  id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  name TEXT NOT NULL,
  relative_path TEXT NOT NULL,
  payload TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY(server_id,id),
  FOREIGN KEY(server_id) REFERENCES connections(server_id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS sessions (
  server_id TEXT NOT NULL,
  id TEXT NOT NULL,
  project_id TEXT NOT NULL,
  title TEXT NOT NULL,
  lifecycle_state TEXT NOT NULL,
  last_message_seq INTEGER NOT NULL,
  last_activity_at TEXT NOT NULL,
  payload TEXT NOT NULL,
  PRIMARY KEY(server_id,id),
  FOREIGN KEY(server_id) REFERENCES connections(server_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS sessions_activity ON sessions(server_id,lifecycle_state,last_activity_at DESC);
CREATE TABLE IF NOT EXISTS session_reads (
  server_id TEXT NOT NULL,
  session_id TEXT NOT NULL,
  last_read_agent_seq INTEGER NOT NULL DEFAULT 0,
  last_read_interactive_id TEXT,
  initialized INTEGER NOT NULL DEFAULT 1,
  updated_at TEXT NOT NULL,
  PRIMARY KEY(server_id,session_id),
  FOREIGN KEY(server_id) REFERENCES connections(server_id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS conversation_snapshots (
  server_id TEXT NOT NULL,
  session_id TEXT NOT NULL,
  session_payload TEXT NOT NULL,
  settings_payload TEXT NOT NULL,
  current_run_payload TEXT,
  snapshot_cursor INTEGER NOT NULL,
  next_cursor TEXT NOT NULL,
  has_more_before INTEGER NOT NULL,
  turns_complete INTEGER NOT NULL DEFAULT 0,
  hydration_state TEXT NOT NULL DEFAULT 'pending',
  byte_size INTEGER NOT NULL,
  last_accessed_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY(server_id,session_id),
  FOREIGN KEY(server_id) REFERENCES connections(server_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS conversation_snapshots_lru
  ON conversation_snapshots(server_id,last_accessed_at);
CREATE TABLE IF NOT EXISTS conversation_turns (
  server_id TEXT NOT NULL,
  session_id TEXT NOT NULL,
  id TEXT NOT NULL,
  kind TEXT NOT NULL,
  anchor_seq INTEGER NOT NULL,
  payload TEXT NOT NULL,
  byte_size INTEGER NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY(server_id,session_id,id),
  FOREIGN KEY(server_id,session_id) REFERENCES conversation_snapshots(server_id,session_id)
    ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS conversation_turns_window
  ON conversation_turns(server_id,session_id,anchor_seq DESC);
CREATE TABLE IF NOT EXISTS segment_cache_state (
  server_id TEXT NOT NULL,
  session_id TEXT NOT NULL,
  run_id TEXT NOT NULL,
  segment_id TEXT NOT NULL,
  persisted_through_event_seq INTEGER NOT NULL DEFAULT 0,
  has_more_before INTEGER NOT NULL DEFAULT 0,
  complete INTEGER NOT NULL DEFAULT 0,
  final_draft TEXT NOT NULL DEFAULT '',
  byte_size INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL,
  PRIMARY KEY(server_id,segment_id),
  FOREIGN KEY(server_id,session_id) REFERENCES conversation_snapshots(server_id,session_id)
    ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS run_activities (
  server_id TEXT NOT NULL,
  session_id TEXT NOT NULL,
  id TEXT NOT NULL,
  segment_id TEXT NOT NULL,
  first_event_sequence INTEGER NOT NULL,
  last_event_sequence INTEGER NOT NULL,
  payload TEXT NOT NULL,
  byte_size INTEGER NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY(server_id,id),
  FOREIGN KEY(server_id,segment_id) REFERENCES segment_cache_state(server_id,segment_id)
    ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS run_activities_window
  ON run_activities(server_id,segment_id,first_event_sequence);
CREATE TABLE IF NOT EXISTS drafts (
  server_id TEXT NOT NULL,
  scope TEXT NOT NULL,
  text TEXT NOT NULL,
  settings TEXT,
  attachment_ids TEXT NOT NULL DEFAULT '[]',
  updated_at TEXT NOT NULL,
  PRIMARY KEY(server_id,scope)
);
CREATE TABLE IF NOT EXISTS outbox (
  server_id TEXT NOT NULL,
  local_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  session_id TEXT,
  project_id TEXT,
  status TEXT NOT NULL CHECK(status IN ('pending','uploading','sending','failed')),
  payload TEXT NOT NULL,
  attempt_count INTEGER NOT NULL DEFAULT 0,
  error TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY(server_id,local_id)
);
CREATE INDEX IF NOT EXISTS outbox_dispatch ON outbox(server_id,status,created_at);
CREATE TABLE IF NOT EXISTS sync_state (
  server_id TEXT PRIMARY KEY,
  cursor INTEGER NOT NULL DEFAULT 0,
  last_synced_at TEXT,
  FOREIGN KEY(server_id) REFERENCES connections(server_id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS app_settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS image_cache_entries (
  server_id TEXT NOT NULL,
  cache_key TEXT NOT NULL,
  uri TEXT NOT NULL,
  media_type TEXT NOT NULL,
  size_bytes INTEGER NOT NULL,
  sha256 TEXT NOT NULL,
  expires_at TEXT,
  last_accessed_at TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY(server_id,cache_key),
  FOREIGN KEY(server_id) REFERENCES connections(server_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS image_cache_lru
  ON image_cache_entries(server_id,last_accessed_at);
`;

export async function getDatabase(): Promise<SQLite.SQLiteDatabase> {
  databasePromise ??= openDatabase();
  return databasePromise;
}

export function runDatabaseWrite<Result>(
  operation: (database: SQLite.SQLiteDatabase) => Promise<Result>,
): Promise<Result> {
  const result = writeQueue.then(async () => operation(await getDatabase()));
  writeQueue = result.then(() => undefined, () => undefined);
  return result;
}

export async function withDatabaseTransaction(
  operation: (database: SQLite.SQLiteDatabase) => Promise<void>,
): Promise<void> {
  await runDatabaseWrite(async (database) => database.withExclusiveTransactionAsync(
    async (transaction) => operation(transaction),
  ));
}

async function openDatabase(): Promise<SQLite.SQLiteDatabase> {
  const database = await SQLite.openDatabaseAsync("tyrs-hand.db");
  await database.execAsync(schema);
  return database;
}

export async function clearServerSnapshot(serverId: string): Promise<void> {
  await withDatabaseTransaction(async (database) => {
    for (const table of ["projects", "sessions", "conversation_snapshots"]) {
      await database.runAsync(`DELETE FROM ${table} WHERE server_id=?`, serverId);
    }
    await database.runAsync("UPDATE sync_state SET cursor=0,last_synced_at=NULL WHERE server_id=?", serverId);
  });
}
