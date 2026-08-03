import * as SQLite from "expo-sqlite";

let databasePromise: Promise<SQLite.SQLiteDatabase> | null = null;
let writeQueue: Promise<void> = Promise.resolve();

const schema = `
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;
CREATE TABLE IF NOT EXISTS connections (
  server_id TEXT PRIMARY KEY,
  base_url TEXT NOT NULL,
  name TEXT NOT NULL,
  device_id TEXT NOT NULL,
  active INTEGER NOT NULL DEFAULT 0,
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
CREATE TABLE IF NOT EXISTS messages (
  server_id TEXT NOT NULL,
  session_id TEXT NOT NULL,
  id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  local_id TEXT NOT NULL,
  role TEXT NOT NULL,
  payload TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY(server_id,id),
  UNIQUE(server_id,session_id,local_id),
  FOREIGN KEY(server_id,session_id) REFERENCES sessions(server_id,id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS messages_window ON messages(server_id,session_id,seq DESC);
CREATE TABLE IF NOT EXISTS attachments (
  server_id TEXT NOT NULL,
  id TEXT NOT NULL,
  session_id TEXT,
  local_id TEXT,
  local_uri TEXT,
  payload TEXT NOT NULL,
  PRIMARY KEY(server_id,id)
);
CREATE TABLE IF NOT EXISTS runs (
  server_id TEXT NOT NULL,
  id TEXT NOT NULL,
  session_id TEXT NOT NULL,
  status TEXT NOT NULL,
  payload TEXT NOT NULL,
  PRIMARY KEY(server_id,id)
);
CREATE TABLE IF NOT EXISTS interactives (
  server_id TEXT NOT NULL,
  id TEXT NOT NULL,
  session_id TEXT NOT NULL,
  status TEXT NOT NULL,
  payload TEXT NOT NULL,
  PRIMARY KEY(server_id,id)
);
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
  await database.execAsync("PRAGMA user_version = 1");
  return database;
}

export async function clearServerSnapshot(serverId: string): Promise<void> {
  await withDatabaseTransaction(async (database) => {
    for (const table of ["projects", "sessions", "messages", "attachments", "runs", "interactives"]) {
      await database.runAsync(`DELETE FROM ${table} WHERE server_id=?`, serverId);
    }
    await database.runAsync("UPDATE sync_state SET cursor=0,last_synced_at=NULL WHERE server_id=?", serverId);
  });
}
