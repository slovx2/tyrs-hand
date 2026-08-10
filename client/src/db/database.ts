import * as SQLite from "expo-sqlite";

export const DATABASE_VERSION = 10;

export function needsThreadHistoryCacheReset(currentVersion: number): boolean {
  return currentVersion >= 4 && currentVersion < 7;
}

let databasePromise: Promise<SQLite.SQLiteDatabase> | null = null;
let writeQueue: Promise<void> = Promise.resolve();

const schema = `
PRAGMA auto_vacuum = INCREMENTAL;
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;
CREATE TABLE IF NOT EXISTS connection_profiles (
  profile_id TEXT PRIMARY KEY,
  kind TEXT NOT NULL CHECK(kind='ssh'),
  name TEXT NOT NULL,
  active INTEGER NOT NULL DEFAULT 0,
  ssh_host TEXT NOT NULL,
  ssh_port INTEGER NOT NULL,
  ssh_user TEXT NOT NULL,
  ssh_key_ref TEXT NOT NULL,
  ssh_host_fingerprint TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
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
CREATE TABLE IF NOT EXISTS app_settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
`;

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
    if (current < 5) await migrateSSHProjects(database);
    if (needsThreadHistoryCacheReset(current)) await migrateThreadHistoryCache(database);
    if (current < 9) await migrateThreadReads(database);
    if (current < 10) await migrateSSHOnly(database);
    if (current < DATABASE_VERSION) {
      await database.execAsync(`PRAGMA user_version = ${DATABASE_VERSION}`);
    }
  }
  return database;
}

async function migrateToOfficialProtocol(database: SQLite.SQLiteDatabase): Promise<void> {
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
        PRAGMA user_version = 5;
      `);
    });
  } finally {
    await database.execAsync("PRAGMA foreign_keys = ON");
  }
}

async function migrateThreadHistoryCache(database: SQLite.SQLiteDatabase): Promise<void> {
  await database.withExclusiveTransactionAsync(async (transaction) => {
    // Thread 历史可以从官方 App Server 重建；v7 清除仍可能包含工具输出的旧缓存。
    await transaction.runAsync("DELETE FROM threads");
    await transaction.execAsync(`PRAGMA user_version = ${DATABASE_VERSION}`);
  });
}

async function migrateThreadReads(database: SQLite.SQLiteDatabase): Promise<void> {
  const now = new Date().toISOString();
  await database.withExclusiveTransactionAsync(async (transaction) => {
    // 升级时已有历史全部视为已读，避免安装新版本后目录一次性出现大量红点。
    await transaction.runAsync(`INSERT OR IGNORE INTO thread_reads(
      profile_id,thread_id,has_unread,updated_at)
      SELECT profile_id,id,0,? FROM threads`, now);
    await transaction.execAsync("PRAGMA user_version = 9");
  });
}

async function migrateSSHOnly(database: SQLite.SQLiteDatabase): Promise<void> {
  await database.execAsync("PRAGMA foreign_keys = OFF");
  try {
    await database.withExclusiveTransactionAsync(async (transaction) => {
      // Control profile 属于已删除的旧移动 App 协议；其本地缓存不可用于官方 SSH profile。
      for (const table of ["ssh_projects", "projects", "threads", "thread_reads", "drafts",
        "pending_submissions", "outbox"]) {
        await transaction.execAsync(`DELETE FROM ${table} WHERE profile_id IN (
          SELECT profile_id FROM connection_profiles WHERE kind<>'ssh')`);
      }
      await transaction.execAsync(`
        DROP INDEX IF EXISTS connection_profiles_one_active;
        CREATE TABLE connection_profiles_v10 (
          profile_id TEXT PRIMARY KEY,
          kind TEXT NOT NULL CHECK(kind='ssh'),
          name TEXT NOT NULL,
          active INTEGER NOT NULL DEFAULT 0,
          ssh_host TEXT NOT NULL,
          ssh_port INTEGER NOT NULL,
          ssh_user TEXT NOT NULL,
          ssh_key_ref TEXT NOT NULL,
          ssh_host_fingerprint TEXT,
          created_at TEXT NOT NULL,
          updated_at TEXT NOT NULL
        );
        INSERT INTO connection_profiles_v10(profile_id,kind,name,active,ssh_host,ssh_port,
          ssh_user,ssh_key_ref,ssh_host_fingerprint,created_at,updated_at)
        SELECT profile_id,'ssh',name,active,ssh_host,ssh_port,ssh_user,ssh_key_ref,
          ssh_host_fingerprint,created_at,updated_at
        FROM connection_profiles WHERE kind='ssh';
        UPDATE connection_profiles_v10 SET active=1
        WHERE profile_id=(SELECT profile_id FROM connection_profiles_v10 ORDER BY name,profile_id LIMIT 1)
          AND NOT EXISTS (SELECT 1 FROM connection_profiles_v10 WHERE active=1);
        DROP TABLE connection_profiles;
        ALTER TABLE connection_profiles_v10 RENAME TO connection_profiles;
        CREATE UNIQUE INDEX connection_profiles_one_active
          ON connection_profiles(active) WHERE active=1;
        PRAGMA user_version = 10;
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
    await database.runAsync("DELETE FROM thread_reads WHERE profile_id=?", profileId);
    await database.runAsync("DELETE FROM pending_submissions WHERE profile_id=?", profileId);
    await database.runAsync("DELETE FROM outbox WHERE profile_id=?", profileId);
  });
}
