import { DatabaseSync, type SQLInputValue } from "node:sqlite";
import { createServer, type Server } from "node:http";
import { afterAll, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

import type { ConversationSnapshotResponse } from "@/types/protocol";

type AsyncDatabase = {
  execAsync: (sql: string) => Promise<void>;
  runAsync: (sql: string, ...parameters: unknown[]) => Promise<unknown>;
  getFirstAsync: <T>(sql: string, ...parameters: unknown[]) => Promise<T | null>;
  getAllAsync: <T>(sql: string, ...parameters: unknown[]) => Promise<T[]>;
  withExclusiveTransactionAsync: (operation: (database: AsyncDatabase) => Promise<void>) => Promise<void>;
};

const testState = vi.hoisted(() => ({ database: null as AsyncDatabase | null, failSql: "" }));

vi.mock("expo-sqlite", () => ({
  openDatabaseAsync: vi.fn(async () => {
    if (!testState.database) throw new Error("测试数据库尚未初始化");
    return testState.database;
  }),
}));

vi.mock("@/db/connections", () => ({ getToken: vi.fn(async () => "integration-token") }));

const serverId = "10000000-0000-4000-8000-000000000001";
const sessionId = "20000000-0000-4000-8000-000000000001";
const projectId = "30000000-0000-4000-8000-000000000001";
const workspaceId = "40000000-0000-4000-8000-000000000001";
const profileId = "50000000-0000-4000-8000-000000000001";
let nativeDatabase: DatabaseSync;

function createAsyncDatabase(database: DatabaseSync): AsyncDatabase {
  const adapter: AsyncDatabase = {
    execAsync: async (sql) => { database.exec(sql); },
    runAsync: async (sql, ...parameters) => {
      if (testState.failSql && sql.includes(testState.failSql)) throw new Error("injected sqlite failure");
      return database.prepare(sql).run(...parameters as SQLInputValue[]);
    },
    getFirstAsync: async <T>(sql: string, ...parameters: unknown[]) =>
      (database.prepare(sql).get(...parameters as SQLInputValue[]) as T | undefined) ?? null,
    getAllAsync: async <T>(sql: string, ...parameters: unknown[]) =>
      database.prepare(sql).all(...parameters as SQLInputValue[]) as T[],
    withExclusiveTransactionAsync: async (operation) => {
      database.exec("BEGIN IMMEDIATE");
      try {
        await operation(adapter);
        database.exec("COMMIT");
      } catch (error) {
        database.exec("ROLLBACK");
        throw error;
      }
    },
  };
  return adapter;
}

function snapshot(cursor = 10, title = "缓存会话"): ConversationSnapshotResponse {
  return {
    session: { id: sessionId, workspaceId, projectId, agentProfileId: profileId, title,
      lifecycleState: "active", historyCompleteness: "complete", model: null,
      reasoningEffort: null, serviceTier: "standard", collaborationMode: "default",
      settingsVersion: 1, lastMessageSeq: 1, isRunning: false, hasRunIssue: false,
      lastAgentMessageSeq: 1, pendingInteractiveId: null, lastActivityAt: "2026-08-05T00:00:00Z",
      createdAt: "2026-08-05T00:00:00Z", updatedAt: "2026-08-05T00:00:00Z" },
    settings: { agentProfileId: profileId, model: null, reasoningEffort: null,
      serviceTier: "standard", collaborationMode: "default", settingsVersion: 1 },
    currentRun: null,
    turns: { items: [{ kind: "message", id: "60000000-0000-4000-8000-000000000001",
      anchorSeq: 1, messages: [{ id: "60000000-0000-4000-8000-000000000001", sessionId,
        seq: 1, localId: "message-1", participantId: null, role: "agent",
        content: { type: "text", text: "缓存内容" }, attachments: [],
        createdAt: "2026-08-05T00:00:00Z", updatedAt: "2026-08-05T00:00:00Z" }], runs: [] }],
      hasMoreBefore: false, nextCursor: "" },
    snapshotCursor: cursor,
  };
}

async function insertConnection(): Promise<void> {
  await testState.database!.runAsync(`INSERT INTO connections(server_id,base_url,name,device_id,active,
    created_at,updated_at) VALUES (?,?,?,?,1,?,?)`, serverId, "http://127.0.0.1", "测试", "device",
  "2026-08-05T00:00:00Z", "2026-08-05T00:00:00Z");
}

beforeAll(async () => {
  nativeDatabase = new DatabaseSync(":memory:");
  testState.database = createAsyncDatabase(nativeDatabase);
  const { getDatabase } = await import("@/db/database");
  await getDatabase();
});

beforeEach(async () => {
  testState.failSql = "";
  nativeDatabase.exec(`DELETE FROM connections;`);
  await insertConnection();
  const { useConversationStore } = await import("./conversationStore");
  useConversationStore.setState({ entries: {} });
});

afterAll(() => nativeDatabase.close());

describe("conversation cache integration", () => {
  it("事务写入并读回完整快照", async () => {
    const { loadConversationWindow, saveConversationSnapshot } = await import("@/db/conversationCache");
    await saveConversationSnapshot(serverId, snapshot());
    const cached = await loadConversationWindow(serverId, sessionId);
    expect(cached?.settings.settingsVersion).toBe(1);
    expect(cached?.turns.items.map((turn) => turn.anchorSeq)).toEqual([1]);
    expect(cached?.snapshotCursor).toBe(10);
  });

  it("按会话 LRU 淘汰且保护当前会话", async () => {
    const { enforceConversationCacheBudget, saveConversationSnapshot } =
      await import("@/db/conversationCache");
    await saveConversationSnapshot(serverId, snapshot());
    const second = snapshot(11, "第二个会话");
    second.session.id = "20000000-0000-4000-8000-000000000002";
    second.turns.items = [];
    await saveConversationSnapshot(serverId, second);
    const evicted = await enforceConversationCacheBudget(serverId, second.session.id, 1);
    expect(evicted).toEqual([sessionId]);
  });

  it("分页写入保持顺序和覆盖边界", async () => {
    const { loadCachedTurnsBefore, loadConversationWindow, saveConversationSnapshot,
      saveConversationTurnPage } = await import("@/db/conversationCache");
    const latest = snapshot();
    latest.turns.items[0]!.anchorSeq = 3;
    latest.turns.hasMoreBefore = true;
    latest.turns.nextCursor = btoa("3");
    await saveConversationSnapshot(serverId, latest);
    const older = [1, 2].map((anchorSeq) => ({ ...latest.turns.items[0]!,
      id: `60000000-0000-4000-8000-${String(anchorSeq).padStart(12, "0")}`, anchorSeq }));
    await saveConversationTurnPage(serverId, sessionId, older, "", false);
    expect((await loadCachedTurnsBefore(serverId, sessionId, 3)).map((turn) => turn.anchorSeq))
      .toEqual([1, 2]);
    expect((await loadConversationWindow(serverId, sessionId))?.turnsComplete).toBe(true);
  });

  it("steer 重归属时原子移除临时孤立轮次", async () => {
    const { loadConversationWindow, saveConversationSnapshot, saveConversationTurn } =
      await import("@/db/conversationCache");
    const current = snapshot();
    await saveConversationSnapshot(serverId, current);
    const orphanId = "60000000-0000-4000-8000-000000000002";
    const orphan = { ...current.turns.items[0]!, id: orphanId, anchorSeq: 2 };
    await saveConversationTurn(serverId, sessionId, orphan);
    await saveConversationTurn(serverId, sessionId, current.turns.items[0]!, orphanId);
    expect((await loadConversationWindow(serverId, sessionId))?.turns.items.map((turn) => turn.id))
      .toEqual([current.turns.items[0]!.id]);
  });

  it("activities 按 segment 持久化并记录完整状态", async () => {
    const { isSegmentCacheComplete, loadCachedSegmentActivities, saveConversationSnapshot,
      saveSegmentActivityPage } = await import("@/db/conversationCache");
    await saveConversationSnapshot(serverId, snapshot());
    const segmentId = "70000000-0000-4000-8000-000000000001";
    await saveSegmentActivityPage(serverId, sessionId, "71000000-0000-4000-8000-000000000001",
      segmentId, { activities: [{ id: "72000000-0000-4000-8000-000000000001", itemId: "item",
        kind: "commentary", firstEventSequence: 1, lastEventSequence: 2, status: "completed",
        payload: { text: "过程" }, occurredAt: "2026-08-05T00:00:00Z" }], hasMoreBefore: false,
        persistedThroughEventSeq: 2, finalAnswerDraft: null }, true);
    expect((await loadCachedSegmentActivities(serverId, segmentId))).toHaveLength(1);
    expect(await isSegmentCacheComplete(serverId, segmentId)).toBe(true);
  });

  it("事务中途失败不会留下半份快照", async () => {
    const { loadConversationWindow, saveConversationSnapshot } = await import("@/db/conversationCache");
    testState.failSql = "INSERT INTO conversation_turns";
    await expect(saveConversationSnapshot(serverId, snapshot())).rejects.toThrow("injected sqlite failure");
    testState.failSql = "";
    expect(await loadConversationWindow(serverId, sessionId)).toBeNull();
  });
});

async function startSnapshotServer(response: ConversationSnapshotResponse,
  delayMs = 0): Promise<{ server: Server; baseUrl: string; requests: string[] }> {
  const requests: string[] = [];
  const server = createServer((request, result) => {
    requests.push(request.url ?? "");
    const send = () => {
      result.statusCode = 200;
      result.setHeader("Content-Type", "application/json");
      if (request.url?.includes("/snapshot")) result.end(JSON.stringify(response));
      else if (request.url?.includes("/turns?")) {
        result.end(JSON.stringify({ items: [], hasMoreBefore: false, nextCursor: "" }));
      } else result.end(JSON.stringify({ activities: [], hasMoreBefore: false, hasMoreAfter: false,
        persistedThroughEventSeq: 0, finalAnswerDraft: null }));
    };
    if (delayMs > 0) setTimeout(send, delayMs); else send();
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const address = server.address();
  if (!address || typeof address === "string") throw new Error("测试 HTTP server 启动失败");
  return { server, baseUrl: `http://127.0.0.1:${address.port}`, requests };
}

describe("conversation repository integration", () => {
  it("通过真实 HTTP 拉取、校验并持久化快照", async () => {
    const remote = await startSnapshotServer(snapshot());
    try {
      const { useConversationStore } = await import("./conversationStore");
      const connection = { serverId, baseUrl: remote.baseUrl, name: "集成", deviceId: "device", active: true };
      await useConversationStore.getState().open(connection, sessionId);
      const entry = useConversationStore.getState().entries[`${serverId}:${sessionId}`];
      expect(entry?.status).toBe("ready");
      expect(entry?.view?.turns).toHaveLength(1);
      expect(remote.requests.filter((url) => url.includes("/snapshot"))).toHaveLength(1);
      const { loadConversationWindow } = await import("@/db/conversationCache");
      expect((await loadConversationWindow(serverId, sessionId))?.session.title).toBe("缓存会话");
    } finally {
      remote.server.close();
    }
  });

  it("弱网刷新相同 snapshotCursor 时保持 view 引用，断网时继续返回缓存", async () => {
    const { saveConversationSnapshot } = await import("@/db/conversationCache");
    await saveConversationSnapshot(serverId, snapshot());
    const remote = await startSnapshotServer(snapshot(10, "同游标网络副本"), 30);
    const { useConversationStore } = await import("./conversationStore");
    const connection = { serverId, baseUrl: remote.baseUrl, name: "集成", deviceId: "device", active: true };
    const opening = useConversationStore.getState().open(connection, sessionId);
    await new Promise((resolve) => setTimeout(resolve, 5));
    const cachedView = useConversationStore.getState().entries[`${serverId}:${sessionId}`]?.view;
    await opening;
    expect(useConversationStore.getState().entries[`${serverId}:${sessionId}`]?.view).toBe(cachedView);
    expect(cachedView?.session.title).toBe("缓存会话");
    await new Promise<void>((resolve) => remote.server.close(() => resolve()));
    useConversationStore.setState({ entries: {} });
    await useConversationStore.getState().open(connection, sessionId);
    const offline = useConversationStore.getState().entries[`${serverId}:${sessionId}`];
    expect(offline?.status).toBe("offline");
    expect(offline?.view?.turns).toHaveLength(1);
  });

  it("网络 snapshotCursor 前进时替换缓存 view", async () => {
    const { saveConversationSnapshot } = await import("@/db/conversationCache");
    await saveConversationSnapshot(serverId, snapshot());
    const remote = await startSnapshotServer(snapshot(11, "网络新快照"), 30);
    try {
      const { useConversationStore } = await import("./conversationStore");
      const connection = { serverId, baseUrl: remote.baseUrl, name: "集成", deviceId: "device", active: true };
      const opening = useConversationStore.getState().open(connection, sessionId);
      await new Promise((resolve) => setTimeout(resolve, 5));
      const cachedView = useConversationStore.getState().entries[`${serverId}:${sessionId}`]?.view;
      await opening;
      const networkView = useConversationStore.getState().entries[`${serverId}:${sessionId}`]?.view;
      expect(networkView).not.toBe(cachedView);
      expect(networkView?.snapshotCursor).toBe(11);
      expect(networkView?.session.title).toBe("网络新快照");
    } finally {
      remote.server.close();
    }
  });

  it("拒绝旧 snapshotCursor 覆盖已处理的 durable 更新", async () => {
    const remote = await startSnapshotServer(snapshot(10), 20);
    try {
      const { useConversationStore } = await import("./conversationStore");
      const connection = { serverId, baseUrl: remote.baseUrl, name: "集成", deviceId: "device", active: true };
      const opening = useConversationStore.getState().open(connection, sessionId);
      useConversationStore.getState().noteCursor(connection, sessionId, 11);
      await opening;
      expect(useConversationStore.getState().entries[`${serverId}:${sessionId}`]?.view).toBeNull();
    } finally {
      remote.server.close();
    }
  });
});
