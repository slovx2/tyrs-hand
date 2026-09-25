import { DatabaseSync, type SQLInputValue } from "node:sqlite";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const harness = vi.hoisted(() => ({ database: null as unknown }));
vi.mock("expo-sqlite", () => ({ openDatabaseAsync: async () => harness.database }));
vi.mock("expo-secure-store", () => ({ WHEN_UNLOCKED_THIS_DEVICE_ONLY: 1,
  setItemAsync: async () => undefined, deleteItemAsync: async () => undefined }));
vi.mock("@/preview/config", () => ({ isPreviewMode: false, isPreviewServerId: () => false }));

function sqliteAdapter(database: DatabaseSync) {
  const adapter = {
    execAsync: async (sql: string) => { database.exec(sql); },
    getFirstAsync: async (sql: string, ...args: SQLInputValue[]) => database.prepare(sql).get(...args) ?? null,
    getAllAsync: async (sql: string, ...args: SQLInputValue[]) => database.prepare(sql).all(...args),
    runAsync: async (sql: string, ...args: SQLInputValue[]) => database.prepare(sql).run(...args),
    withExclusiveTransactionAsync: async (callback: (transaction: { execAsync(sql: string): Promise<void> }) => Promise<void>) => {
      database.exec("BEGIN IMMEDIATE");
      try { await callback(adapter); database.exec("COMMIT"); }
      catch (error) { database.exec("ROLLBACK"); throw error; }
    },
  };
  return adapter;
}

let database: DatabaseSync;
beforeEach(() => {
  vi.resetModules();
  database = new DatabaseSync(":memory:");
  harness.database = sqliteAdapter(database);
});
afterEach(() => database.close());

const sshInput = { kind: "ssh" as const, profileId: "codex-profile", name: "Codex", engine: "codex" as const,
  workerId: "worker-1", host: "localhost", port: 2222, user: "worker", keyRef: "codex-key",
  hostFingerprint: "SHA256:forced-same-fingerprint", privateKey: "virtual-private-key" };

describe("MIGRATION / ISOLATION：真实 SQLite 运行时身份", () => {
  it("旧 Codex profile、草稿和未确认提交原地保留，同 Worker 的 Claude 另建入口", async () => {
    database.exec(`
      PRAGMA foreign_keys=ON;
      CREATE TABLE connection_profiles (
        profile_id TEXT PRIMARY KEY,kind TEXT NOT NULL,name TEXT NOT NULL,active INTEGER NOT NULL,
        machine_fingerprint TEXT NOT NULL,ssh_host TEXT,ssh_port INTEGER,ssh_user TEXT,ssh_key_ref TEXT,
        ssh_host_fingerprint TEXT,created_at TEXT NOT NULL,updated_at TEXT NOT NULL
      );
      CREATE UNIQUE INDEX connection_profiles_machine_fingerprint ON connection_profiles(machine_fingerprint);
      CREATE TABLE control_machine_links (
        profile_id TEXT NOT NULL,server_id TEXT NOT NULL,base_url TEXT NOT NULL,worker_id TEXT NOT NULL,
        worker_name TEXT NOT NULL,device_id TEXT NOT NULL,created_at TEXT NOT NULL,updated_at TEXT NOT NULL,
        PRIMARY KEY(server_id,worker_id),UNIQUE(profile_id,server_id),
        FOREIGN KEY(profile_id) REFERENCES connection_profiles(profile_id) ON DELETE CASCADE
      );
      CREATE TABLE drafts (
        profile_id TEXT NOT NULL,scope TEXT NOT NULL,text TEXT NOT NULL,settings TEXT,
        attachment_ids TEXT NOT NULL DEFAULT '[]',updated_at TEXT NOT NULL,
        PRIMARY KEY(profile_id,scope),FOREIGN KEY(profile_id) REFERENCES connection_profiles(profile_id) ON DELETE CASCADE
      );
      CREATE TABLE pending_submissions (
        profile_id TEXT NOT NULL,client_message_id TEXT NOT NULL,thread_id TEXT,project_id TEXT,
        payload TEXT NOT NULL,state TEXT NOT NULL,error TEXT,created_at TEXT NOT NULL,updated_at TEXT NOT NULL,
        PRIMARY KEY(profile_id,client_message_id),FOREIGN KEY(profile_id) REFERENCES connection_profiles(profile_id) ON DELETE CASCADE
      );
      INSERT INTO connection_profiles VALUES ('codex-profile','machine','原连接',1,'SHA256:forced-same-fingerprint',
        'localhost',2222,'worker','codex-key','SHA256:forced-same-fingerprint','now','now');
      INSERT INTO control_machine_links VALUES ('codex-profile','server-1','https://control.test',
        'worker-1','Worker','device-1','now','now');
      INSERT INTO drafts VALUES ('codex-profile','thread-same','原草稿',NULL,'[]','now');
      INSERT INTO pending_submissions VALUES ('codex-profile','message-same','thread-same',NULL,'{}','unknown',NULL,'now','now');
      PRAGMA user_version=12;
    `);
    const { getDatabase } = await import("./database");
    await getDatabase();
    expect(database.prepare("PRAGMA user_version").get()).toMatchObject({ user_version: 13 });
    expect(database.prepare("SELECT engine,worker_id FROM connection_profiles").get())
      .toMatchObject({ engine: "codex", worker_id: "worker-1" });
    expect(database.prepare("SELECT text FROM drafts").get()).toMatchObject({ text: "原草稿" });
    expect(database.prepare("SELECT state FROM pending_submissions").get()).toMatchObject({ state: "unknown" });
    await assertSeparateClaude();
    expect(database.prepare("PRAGMA foreign_key_check").all()).toEqual([]);
  });

  it("新安装同样允许双入口，并拒绝跨引擎或不同 Worker 的重连身份", async () => {
    const { saveSSHConnection, bindRuntimeIdentity } = await import("./connections");
    await saveSSHConnection(sshInput);
    await assertSeparateClaude();
    const runtime = { workerId: "worker-1", engine: "claude-code" as const,
      protocolVersion: "0.147.0" as const, status: "running" as const, capabilities: [], releaseReady: false };
    await expect(bindRuntimeIdentity("codex-profile", runtime)).rejects.toThrow("不一致");
    await expect(bindRuntimeIdentity("claude-profile", { ...runtime, workerId: "worker-2" })).rejects.toThrow("不一致");
    await expect(bindRuntimeIdentity("claude-profile", runtime)).resolves.toBeUndefined();
  });
});

async function assertSeparateClaude() {
  const { saveSSHConnection, saveControlMachineLink, listConnections } = await import("./connections");
  expect(await saveSSHConnection({ ...sshInput, profileId: "claude-profile", engine: "claude-code",
    name: "Claude", port: 3333, keyRef: "claude-key" })).toBe("claude-profile");
  for (const engine of ["codex", "claude-code"] as const) {
    await saveControlMachineLink({ engine, profileId: engine === "codex" ? "codex-profile" : "claude-profile",
      machineFingerprint: sshInput.hostFingerprint, name: engine, workerId: "worker-1",
      workerName: "Worker", serverId: "server-1", baseUrl: "https://control.test", deviceId: "device-1" });
  }
  const connections = await listConnections();
  expect(connections).toHaveLength(2);
  expect(connections.find((item) => item.engine === "codex")?.controls[0]?.engine).toBe("codex");
  expect(connections.find((item) => item.engine === "claude-code")?.controls[0]?.engine).toBe("claude-code");
  expect(database.prepare("SELECT count(*) AS count FROM control_machine_links").get()).toMatchObject({ count: 2 });
  database.prepare("INSERT INTO drafts VALUES (?,?,?,?,?,?)").run(
    "claude-profile", "thread-same", "Claude 草稿", null, "[]", "now");
  database.prepare("INSERT INTO pending_submissions VALUES (?,?,?,?,?,?,?,?,?)").run(
    "claude-profile", "message-same", "thread-same", null, "{}", "unknown", null, "now", "now");
  expect(database.prepare("SELECT text FROM drafts WHERE profile_id='claude-profile'").get())
    .toMatchObject({ text: "Claude 草稿" });
}
