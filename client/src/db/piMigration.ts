import type * as SQLite from "expo-sqlite";

// 重建受 CHECK 约束的两张表；子表仍引用原表名，不重命名旧表以免改写外键。
export async function migratePiRuntime(database: SQLite.SQLiteDatabase): Promise<void> {
  await database.execAsync("PRAGMA foreign_keys = OFF");
  try {
    await database.withExclusiveTransactionAsync(async (transaction) => {
      await transaction.execAsync(`
        CREATE TABLE connection_profiles_v14 (
          profile_id TEXT PRIMARY KEY,
          kind TEXT NOT NULL CHECK(kind='machine'), name TEXT NOT NULL,
          active INTEGER NOT NULL DEFAULT 0, machine_fingerprint TEXT NOT NULL,
          ssh_host TEXT, ssh_port INTEGER, ssh_user TEXT, ssh_key_ref TEXT,
          ssh_host_fingerprint TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
          engine TEXT NOT NULL DEFAULT 'codex' CHECK(engine IN ('codex','claude-code','pi')),
          worker_id TEXT,
          CHECK((ssh_host IS NULL AND ssh_port IS NULL AND ssh_user IS NULL AND ssh_key_ref IS NULL)
            OR (ssh_host IS NOT NULL AND ssh_port IS NOT NULL AND ssh_user IS NOT NULL AND ssh_key_ref IS NOT NULL))
        );
        INSERT INTO connection_profiles_v14 SELECT profile_id,kind,name,active,machine_fingerprint,
          ssh_host,ssh_port,ssh_user,ssh_key_ref,ssh_host_fingerprint,created_at,updated_at,engine,worker_id
          FROM connection_profiles;
        CREATE TABLE control_machine_links_v14 (
          profile_id TEXT NOT NULL, server_id TEXT NOT NULL, base_url TEXT NOT NULL,
          worker_id TEXT NOT NULL, engine TEXT NOT NULL CHECK(engine IN ('codex','claude-code','pi')),
          worker_name TEXT NOT NULL, device_id TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
          PRIMARY KEY(server_id,worker_id,engine), UNIQUE(profile_id,server_id),
          FOREIGN KEY(profile_id,engine) REFERENCES connection_profiles(profile_id,engine) ON DELETE CASCADE
        );
        INSERT INTO control_machine_links_v14 SELECT * FROM control_machine_links;
        DROP TABLE control_machine_links;
        DROP TABLE connection_profiles;
        ALTER TABLE connection_profiles_v14 RENAME TO connection_profiles;
        ALTER TABLE control_machine_links_v14 RENAME TO control_machine_links;
        CREATE UNIQUE INDEX connection_profiles_one_active ON connection_profiles(active) WHERE active=1;
        CREATE UNIQUE INDEX connection_profiles_machine_fingerprint ON connection_profiles(machine_fingerprint,engine);
        CREATE UNIQUE INDEX connection_profiles_runtime ON connection_profiles(profile_id,engine);
        CREATE TRIGGER connection_profiles_engine_immutable BEFORE UPDATE OF engine ON connection_profiles
          WHEN NEW.engine <> OLD.engine BEGIN SELECT RAISE(ABORT,'运行时引擎不可变'); END;
      `);
      const violations = await transaction.getAllAsync("PRAGMA foreign_key_check");
      if (violations.length) throw new Error("Pi 数据库迁移违反运行时外键");
      await transaction.execAsync("PRAGMA user_version = 14");
    });
  } finally {
    await database.execAsync("PRAGMA foreign_keys = ON");
  }
}
