import { DatabaseSync, type SQLInputValue } from "node:sqlite";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { ThreadRecord } from "@/app-server/types";
import { replaceCachedThreads, saveThreadRecords } from "./cache";

const harness = vi.hoisted(() => ({
  transaction: vi.fn(),
}));
vi.mock("@/preview/config", () => ({ isPreviewMode: false }));
vi.mock("./database", () => ({
  getDatabase: vi.fn(),
  withDatabaseTransaction: harness.transaction,
}));

let database: DatabaseSync;
let writes: number;

beforeEach(() => {
  database = new DatabaseSync(":memory:");
  database.exec(`CREATE TABLE threads (
    profile_id TEXT NOT NULL, id TEXT NOT NULL, archived INTEGER NOT NULL,
    updated_at INTEGER NOT NULL, payload TEXT NOT NULL, PRIMARY KEY (profile_id,id)
  )`);
  writes = 0;
  harness.transaction.mockClear();
  harness.transaction.mockImplementation(async (operation: (db: unknown) => Promise<void>) => {
    database.exec("BEGIN");
    try {
      await operation({ runAsync: async (sql: string, ...params: SQLInputValue[]) => {
        writes++;
        return database.prepare(sql).run(...params);
      } });
      database.exec("COMMIT");
    } catch (error) {
      database.exec("ROLLBACK");
      throw error;
    }
  });
});
afterEach(() => database.close());

describe("会话目录批量缓存", () => {
  it("百条目录只排一个事务，跨批次写入并完整保留更新内容", async () => {
    const records = Array.from({ length: 121 }, (_, index) => record(`thread-${index}`));
    await saveThreadRecords("profile-a", records);
    expect(harness.transaction).toHaveBeenCalledTimes(1);
    expect(writes).toBeLessThanOrEqual(3);
    expect(database.prepare("SELECT count(*) AS n FROM threads").get()?.n).toBe(121);

    const updated = record("thread-60");
    updated.archived = true;
    updated.thread.name = "中文标题和 ' 引号";
    updated.thread.updatedAt = 9;
    await saveThreadRecords("profile-a", [updated]);
    const row = database.prepare("SELECT archived,updated_at,payload FROM threads WHERE id=?")
      .get("thread-60")!;
    expect(row.archived).toBe(1);
    expect(row.updated_at).toBe(9);
    expect(JSON.parse(row.payload as string).thread.name).toBe("中文标题和 ' 引号");
  });

  it("替换目录不会删除其他机器的缓存，空目录会清除当前机器", async () => {
    await saveThreadRecords("profile-a", [record("old")]);
    await saveThreadRecords("profile-b", [record("other")]);
    await replaceCachedThreads("profile-a", [record("new")]);
    expect(database.prepare("SELECT id FROM threads ORDER BY id").all().map((row) => row.id))
      .toEqual(["new", "other"]);
    await replaceCachedThreads("profile-a", []);
    expect(database.prepare("SELECT id FROM threads").all().map((row) => row.id)).toEqual(["other"]);
  });

  it("后续批次失败时回滚整个替换，保留此前目录", async () => {
    await saveThreadRecords("profile-a", [record("old")]);
    const records = Array.from({ length: 51 }, (_, index) => record(`new-${index}`));
    // 在第二批制造约束错误，确认第一批插入和原目录删除一起回滚。
    records[50]!.thread.updatedAt = null as unknown as number;
    await expect(replaceCachedThreads("profile-a", records)).rejects.toThrow();
    expect(database.prepare("SELECT id FROM threads").all().map((row) => row.id)).toEqual(["old"]);
  });
});

function record(id: string): ThreadRecord {
  return {
    archived: false, workspaceId: null, projectId: "project-1", history: { kind: "summary" },
    thread: {
      id, sessionId: id, forkedFromId: null, parentThreadId: null, preview: "测试内容",
      ephemeral: false, section: null, sectionEnteredAt: null, modelProvider: "test",
      createdAt: 1, updatedAt: 2, recencyAt: 2, status: { type: "idle" }, path: null,
      cwd: "/workspace", cliVersion: "test", source: "appServer", threadSource: null,
      agentNickname: null, agentRole: null, gitInfo: null, name: null, turns: [], extra: null,
      historyMode: "legacy", canAcceptDirectInput: true,
    },
  };
}
