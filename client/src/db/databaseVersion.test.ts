import { DatabaseSync } from "node:sqlite";
import { describe, expect, it, vi } from "vitest";

import { DATABASE_VERSION, MACHINE_PROFILE_MIGRATION_SQL,
  needsThreadHistoryCacheReset } from "./database";

vi.mock("expo-sqlite", () => ({}));

describe("数据库 v11 机器身份迁移", () => {
  it("只让可升级的官方协议缓存执行一次失效", () => {
    expect(DATABASE_VERSION).toBe(11);
    expect(needsThreadHistoryCacheReset(3)).toBe(false);
    expect(needsThreadHistoryCacheReset(4)).toBe(true);
    expect(needsThreadHistoryCacheReset(5)).toBe(true);
    expect(needsThreadHistoryCacheReset(6)).toBe(true);
    expect(needsThreadHistoryCacheReset(7)).toBe(false);
    expect(needsThreadHistoryCacheReset(8)).toBe(false);
    expect(needsThreadHistoryCacheReset(9)).toBe(false);
    expect(needsThreadHistoryCacheReset(10)).toBe(false);
    expect(needsThreadHistoryCacheReset(11)).toBe(false);
  });

  it("把 v10 SSH profile 原地升级为机器 profile 并保留缓存", () => {
    const database = new DatabaseSync(":memory:");
    try {
      database.exec(`
        PRAGMA foreign_keys = ON;
        CREATE TABLE connection_profiles (
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
        CREATE UNIQUE INDEX connection_profiles_one_active
          ON connection_profiles(active) WHERE active=1;
        CREATE TABLE threads (
          profile_id TEXT NOT NULL,
          id TEXT NOT NULL,
          payload TEXT NOT NULL,
          PRIMARY KEY(profile_id,id),
          FOREIGN KEY(profile_id) REFERENCES connection_profiles(profile_id) ON DELETE CASCADE
        );
        INSERT INTO connection_profiles VALUES
          ('verified','ssh','Verified',1,'worker.test',2222,'codex','key-a',
            'SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA','2026-01-01','2026-01-01'),
          ('legacy','ssh','Legacy',0,'legacy.test',22,'codex','key-b',NULL,
            '2026-01-01','2026-01-01');
        INSERT INTO threads VALUES ('verified','thread-1','{}');
        PRAGMA user_version = 10;
        PRAGMA foreign_keys = OFF;
      `);
      database.exec(MACHINE_PROFILE_MIGRATION_SQL);
      database.exec("PRAGMA foreign_keys = ON");

      const verified = database.prepare(`SELECT profile_id,kind,machine_fingerprint,
        ssh_host FROM connection_profiles WHERE profile_id='verified'`).get() as {
          profile_id: string;
          kind: string;
          machine_fingerprint: string;
          ssh_host: string;
        };
      expect(verified).toEqual({ profile_id: "verified", kind: "machine",
        machine_fingerprint: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
        ssh_host: "worker.test" });
      expect(database.prepare(`SELECT machine_fingerprint FROM connection_profiles
        WHERE profile_id='legacy'`).get()).toMatchObject({
        machine_fingerprint: "unverified:legacy",
      });
      expect(database.prepare("SELECT count(*) AS count FROM threads").get()).toMatchObject({ count: 1 });
      expect(database.prepare("PRAGMA user_version").get()).toMatchObject({ user_version: 11 });

      database.prepare(`INSERT INTO control_machine_links(profile_id,server_id,base_url,
        worker_id,worker_name,device_id,created_at,updated_at)
        VALUES (?,?,?,?,?,?,?,?)`).run("verified", "control-1", "https://control.test",
        "worker-1", "Worker", "device-1", "2026-01-01", "2026-01-01");
      expect(database.prepare("SELECT count(*) AS count FROM control_machine_links").get())
        .toMatchObject({ count: 1 });
    } finally {
      database.close();
    }
  });
});
