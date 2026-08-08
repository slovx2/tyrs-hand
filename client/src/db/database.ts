import * as SQLite from "expo-sqlite";

const DATABASE_VERSION = 5;

let databasePromise: Promise<SQLite.SQLiteDatabase> | null = null;
let writeQueue: Promise<void> = Promise.resolve();

const schema = `
PRAGMA auto_vacuum = INCREMENTAL;
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;
CREATE TABLE IF NOT EXISTS connection_profiles (
  profile_id TEXT PRIMARY KEY,
  kind TEXT NOT NULL CHECK(kind IN ('control','ssh')),
  name TEXT NOT NULL,
  active INTEGER NOT NULL DEFAULT 0,
  control_server_id TEXT,
  control_base_url TEXT,
  control_device_id TEXT,
  ssh_host TEXT,
  ssh_port INTEGER,
  ssh_user TEXT,
  ssh_key_ref TEXT,
  ssh_host_fingerprint TEXT,
  bootstrap_payload TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  CHECK((kind='control' AND control_server_id IS NOT NULL AND control_base_url IS NOT NULL
    AND control_device_id IS NOT NULL AND ssh_host IS NULL)
    OR (kind='ssh' AND ssh_host IS NOT NULL AND ssh_port IS NOT NULL AND ssh_user IS NOT NULL
    AND ssh_key_ref IS NOT NULL AND control_server_id IS NULL))
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
CREATE TABLE IF NOT EXISTS app_settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
`;

type LegacyConnection = {
  server_id: string;
  base_url: string;
  name: string;
  device_id: string;
  active: number;
  bootstrap_payload: string | null;
  created_at: string;
  updated_at: string;
};

type LegacySSHProject = {
  profile_id: string;
  remote_path: string;
  created_at: string;
  updated_at: string;
};

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
  const version = await database.getFirstAsync<{ user_version: number }>("PRAGMA user_version");
  const current = version?.user_version ?? 0;
  if (current > DATABASE_VERSION) throw new Error("本地数据库版本高于当前客户端");
  if (current < 4) {
    await migrateToOfficialProtocol(database);
  } else {
    await database.execAsync(schema);
    if (current < DATABASE_VERSION) await migrateSSHProjects(database);
  }
  return database;
}

async function migrateToOfficialProtocol(database: SQLite.SQLiteDatabase): Promise<void> {
  let legacy: LegacyConnection[] = [];
  try {
    legacy = await database.getAllAsync<LegacyConnection>(
      `SELECT server_id,base_url,name,device_id,active,bootstrap_payload,created_at,updated_at
       FROM connections`,
    );
  } catch {
    // 新安装没有旧表。
  }
  await database.execAsync(`
    PRAGMA foreign_keys = OFF;
    DROP TABLE IF EXISTS run_activities;
    DROP TABLE IF EXISTS segment_cache_state;
    DROP TABLE IF EXISTS conversation_turns;
    DROP TABLE IF EXISTS conversation_snapshots;
    DROP TABLE IF EXISTS session_reads;
    DROP TABLE IF EXISTS sessions;
    DROP TABLE IF EXISTS outbox;
    DROP TABLE IF EXISTS sync_state;
    DROP TABLE IF EXISTS image_cache_entries;
    DROP TABLE IF EXISTS projects;
    DROP TABLE IF EXISTS drafts;
    DROP TABLE IF EXISTS connections;
    PRAGMA foreign_keys = ON;
  `);
  await database.execAsync(schema);
  for (const row of legacy) {
    await database.runAsync(`INSERT INTO connection_profiles(
      profile_id,kind,name,active,control_server_id,control_base_url,control_device_id,
      bootstrap_payload,created_at,updated_at) VALUES (?,'control',?,?,?,?,?,?,?,?)`,
    row.server_id, row.name, row.active, row.server_id, row.base_url, row.device_id,
    row.bootstrap_payload, row.created_at, row.updated_at);
  }
  await database.execAsync(`PRAGMA user_version = ${DATABASE_VERSION}`);
}

async function migrateSSHProjects(database: SQLite.SQLiteDatabase): Promise<void> {
  const roots = await database.getAllAsync<LegacySSHProject>(`SELECT profile_id,
    ssh_remote_project_root AS remote_path,created_at,updated_at FROM connection_profiles
    WHERE kind='ssh' AND trim(ssh_remote_project_root)<>''`);
  await database.execAsync("PRAGMA foreign_keys = OFF");
  try {
    await database.withExclusiveTransactionAsync(async (transaction) => {
      for (const root of roots) {
        const normalized = root.remote_path.trim().replace(/\/+$/, "") || "/";
        await transaction.runAsync(`INSERT OR IGNORE INTO ssh_projects(
          profile_id,id,remote_path,created_at,updated_at) VALUES (?,?,?,?,?)`,
        root.profile_id, root.profile_id, normalized, root.created_at, root.updated_at);
      }
      await transaction.execAsync(`
        CREATE TABLE connection_profiles_v5 (
          profile_id TEXT PRIMARY KEY,
          kind TEXT NOT NULL CHECK(kind IN ('control','ssh')),
          name TEXT NOT NULL,
          active INTEGER NOT NULL DEFAULT 0,
          control_server_id TEXT,
          control_base_url TEXT,
          control_device_id TEXT,
          ssh_host TEXT,
          ssh_port INTEGER,
          ssh_user TEXT,
          ssh_key_ref TEXT,
          ssh_host_fingerprint TEXT,
          bootstrap_payload TEXT,
          created_at TEXT NOT NULL,
          updated_at TEXT NOT NULL,
          CHECK((kind='control' AND control_server_id IS NOT NULL AND control_base_url IS NOT NULL
            AND control_device_id IS NOT NULL AND ssh_host IS NULL)
            OR (kind='ssh' AND ssh_host IS NOT NULL AND ssh_port IS NOT NULL
            AND ssh_user IS NOT NULL AND ssh_key_ref IS NOT NULL AND control_server_id IS NULL))
        );
        INSERT INTO connection_profiles_v5(profile_id,kind,name,active,control_server_id,
          control_base_url,control_device_id,ssh_host,ssh_port,ssh_user,ssh_key_ref,
          ssh_host_fingerprint,bootstrap_payload,created_at,updated_at)
        SELECT profile_id,kind,name,active,control_server_id,control_base_url,control_device_id,
          ssh_host,ssh_port,ssh_user,ssh_key_ref,ssh_host_fingerprint,bootstrap_payload,
          created_at,updated_at FROM connection_profiles;
        DROP TABLE connection_profiles;
        ALTER TABLE connection_profiles_v5 RENAME TO connection_profiles;
        CREATE UNIQUE INDEX connection_profiles_one_active
          ON connection_profiles(active) WHERE active=1;
        PRAGMA user_version = ${DATABASE_VERSION};
      `);
    });
  } finally {
    await database.execAsync("PRAGMA foreign_keys = ON");
  }
}

export async function clearProfileCache(profileId: string): Promise<void> {
  await withDatabaseTransaction(async (database) => {
    await database.runAsync("DELETE FROM projects WHERE profile_id=?", profileId);
    await database.runAsync("DELETE FROM threads WHERE profile_id=?", profileId);
    await database.runAsync("DELETE FROM pending_submissions WHERE profile_id=?", profileId);
  });
}
