import type { ServerRequest } from "@codex-app-server/ServerRequest";
import type { Thread } from "@codex-app-server/v2/Thread";
import type { Turn } from "@codex-app-server/v2/Turn";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { OfficialTurnPage, ResumedThreadPage } from "@/app-server/officialClient";
import type { ThreadRecord } from "@/app-server/types";
import type { Connection } from "@/db/connections";
import { useAppStore } from "./appStore";

const harness = vi.hoisted(() => ({
  client: null as unknown,
  saved: [] as unknown[],
}));

vi.mock("expo-crypto", () => ({ randomUUID: () => "message-id" }));
vi.mock("@/api/control", () => ({ ControlApi: class {} }));
vi.mock("@/app-server/attachments", () => ({
  materializeUserInput: async () => [],
}));
vi.mock("@/app-server/registry", () => ({
  officialClientFor: () => harness.client,
}));
vi.mock("@/app-server/outbox", () => ({
  completeOutbox: async () => undefined,
  discardOutboxItem: async () => undefined,
  enqueueOutbox: async () => undefined,
  failOutbox: async () => undefined,
  listOutbox: async () => [],
  markOutboxProcessing: async () => undefined,
  retryOutboxItem: async () => undefined,
  setOutboxThread: async () => undefined,
}));
vi.mock("@/db/cache", () => ({
  loadCachedProjects: async () => [],
  loadCachedThreads: async () => [],
  replaceCachedThreads: async () => undefined,
  saveProjects: async () => undefined,
  saveThreadRecord: async (_profileId: string, record: unknown) => { harness.saved.push(record); },
}));
vi.mock("@/db/connections", () => ({
  listConnections: async () => [],
  setActiveConnection: async () => undefined,
}));
vi.mock("@/db/sshProjects", () => ({ listSSHProjects: async () => [] }));
vi.mock("@/db/settings", () => ({
  loadThemeMode: async () => "system",
  saveLastTurnPreferences: async () => undefined,
  saveThemeMode: async () => undefined,
}));

class FakeOfficialClient {
  resume!: (threadId: string) => Promise<ResumedThreadPage>;
  listPage!: (threadId: string, cursor: string | null) => Promise<OfficialTurnPage>;
  metadata!: (threadId: string) => Promise<Thread>;
  readonly pageCalls: (string | null)[] = [];
  private listener: ((event: { method: string; params: unknown }) => void) | null = null;

  async connect(): Promise<void> {}
  async resumeThreadPage(threadId: string): Promise<ResumedThreadPage> {
    return this.resume(threadId);
  }
  async listTurnPage(threadId: string, cursor: string | null): Promise<OfficialTurnPage> {
    this.pageCalls.push(cursor);
    return this.listPage(threadId, cursor);
  }
  async readThreadMetadata(threadId: string): Promise<Thread> { return this.metadata(threadId); }
  subscribe(listener: (event: { method: string; params: unknown }) => void): () => void {
    this.listener = listener;
    return () => { this.listener = null; };
  }
  pendingRequests(): ServerRequest[] { return []; }
  emit(method: string, threadId: string): void {
    this.listener?.({ method, params: { threadId } });
  }
  emitName(threadId: string, threadName: string): void {
    this.listener?.({ method: "thread/name/updated", params: { threadId, threadName } });
  }
}

describe("会话分页 Repository", () => {
  beforeEach(() => { harness.saved = []; });

  it("首次只落最近 5 个 Turn，旧页按游标前插并保持时间顺序", async () => {
    const profileId = "profile-first";
    const threadId = "thread-first";
    const client = new FakeOfficialClient();
    client.resume = async () => ({ thread: thread(threadId, turns(6, 10)),
      page: page(turns(6, 10), "older-1") });
    client.listPage = async (_id, cursor) => {
      expect(cursor).toBe("older-1");
      return page(turns(1, 5), null);
    };
    client.metadata = async () => thread(threadId, []);
    installState(profileId, summaryRecord(threadId), client);

    await useAppStore.getState().loadThread(threadId);
    expect(currentRecord(threadId).thread.turns.map((item) => item.id)).toEqual(
      ["turn-6", "turn-7", "turn-8", "turn-9", "turn-10"]);

    await useAppStore.getState().loadOlderThread(threadId);
    expect(currentRecord(threadId).thread.turns.map((item) => item.id)).toEqual(
      turns(1, 10).map((item) => item.id));
    expect(currentRecord(threadId).history).toMatchObject({
      olderCursor: null, hasLoadedOldest: true,
    });
  });

  it("旧页与尾部刷新并发时都保留，过期响应不能回滚新尾部", async () => {
    const profileId = "profile-concurrent";
    const threadId = "thread-concurrent";
    const client = new FakeOfficialClient();
    const older = deferred<OfficialTurnPage>();
    client.resume = async () => ({ thread: thread(threadId, turns(6, 10)),
      page: page(turns(6, 10), "older-1") });
    client.listPage = async (_id, cursor) => cursor === "older-1"
      ? older.promise : page(turns(8, 12), "tail-older");
    client.metadata = async () => thread(threadId, []);
    installState(profileId, loadedRecord(threadId, turns(6, 10), "older-1"), client);

    const olderRequest = useAppStore.getState().loadOlderThread(threadId);
    await Promise.resolve();
    await useAppStore.getState().refreshThreadTail(threadId);
    older.resolve(page(turns(1, 5), null));
    await olderRequest;

    expect(currentRecord(threadId).thread.turns.map((item) => item.id))
      .toEqual(turns(1, 12).map((item) => item.id));
  });

  it("尾部刷新执行期间的重复失效信号只触发一次串行追赶", async () => {
    const profileId = "profile-coalesce";
    const threadId = "thread-coalesce";
    const client = new FakeOfficialClient();
    const firstPage = deferred<OfficialTurnPage>();
    let tailCalls = 0;
    client.resume = async () => ({ thread: thread(threadId, turns(1, 5)),
      page: page(turns(1, 5), null) });
    client.listPage = async () => ++tailCalls === 1
      ? firstPage.promise : page([turn(5, "completed", "updated"), turn(6)], null);
    client.metadata = async () => thread(threadId, []);
    installState(profileId, loadedRecord(threadId, turns(1, 5), null), client);

    const first = useAppStore.getState().refreshThreadTail(threadId);
    await Promise.resolve();
    const second = useAppStore.getState().refreshThreadTail(threadId);
    const third = useAppStore.getState().refreshThreadTail(threadId);
    firstPage.resolve(page(turns(1, 5), null));
    await Promise.all([first, second, third]);

    expect(tailCalls).toBe(2);
    expect(currentRecord(threadId).thread.turns.at(-1)?.id).toBe("turn-6");
    expect(currentRecord(threadId).thread.turns.find((item) => item.id === "turn-5")
      ?.items[0]?.id).toBe("item:updated");
  });

  it("最新页无重叠时不扫描旧页，重置到新的官方分页边界", async () => {
    const profileId = "profile-gap";
    const threadId = "thread-gap";
    const client = new FakeOfficialClient();
    client.resume = async () => ({ thread: thread(threadId, turns(8, 12)),
      page: page(turns(8, 12), "new-older") });
    client.listPage = async (_id, cursor) => {
      expect(cursor).toBeNull();
      return page(turns(8, 12), "new-older");
    };
    client.metadata = async () => thread(threadId, []);
    installState(profileId, loadedRecord(threadId, turns(1, 2), "old-older"), client);

    await useAppStore.getState().refreshThreadTail(threadId);

    expect(client.pageCalls).toEqual([null]);
    expect(currentRecord(threadId).thread.turns.map((item) => item.id))
      .toEqual(turns(8, 12).map((item) => item.id));
    expect(currentRecord(threadId).history).toMatchObject({
      olderCursor: "new-older", tailOlderCursor: "new-older", hasLoadedOldest: false,
    });
  });

  it("连续流式通知使用前沿节流，不会因持续 delta 永远推迟刷新", async () => {
    vi.useFakeTimers();
    try {
      const profileId = "profile-stream";
      const threadId = "thread-stream";
      const client = new FakeOfficialClient();
      client.resume = async () => ({ thread: thread(threadId, turns(1, 5)),
        page: page(turns(1, 5), null) });
      client.listPage = async () => page(turns(1, 5), null);
      client.metadata = async () => thread(threadId, []);
      installState(profileId, loadedRecord(threadId, turns(1, 5), null), client);
      await useAppStore.getState().refreshThreadTail(threadId);
      client.pageCalls.length = 0;

      client.emit("item/agentMessage/delta", threadId);
      await vi.advanceTimersByTimeAsync(60);
      client.emit("item/agentMessage/delta", threadId);
      await vi.advanceTimersByTimeAsync(60);
      client.emit("item/agentMessage/delta", threadId);
      await Promise.resolve();

      expect(client.pageCalls).toEqual([null]);
    } finally {
      vi.useRealTimers();
    }
  });

  it("完成通知丢失时，最新页权威对账会结束活动 Turn", async () => {
    const profileId = "profile-missed-completion";
    const threadId = "thread-missed-completion";
    const client = new FakeOfficialClient();
    client.resume = async () => ({ thread: thread(threadId, turns(1, 5)),
      page: page(turns(1, 5), null) });
    client.listPage = async () => page([
      ...turns(1, 4), turn(5, "completed", "final-answer"),
    ], null);
    client.metadata = async () => ({ ...thread(threadId, []), status: { type: "idle" } });
    installState(profileId,
      loadedRecord(threadId, [...turns(1, 4), turn(5, "inProgress", "streaming")], null),
      client);

    expect(currentRecord(threadId).thread.turns.at(-1)?.status).toBe("inProgress");
    await useAppStore.getState().refreshThreadTail(threadId);

    expect(currentRecord(threadId).thread.turns.at(-1)).toMatchObject({
      id: "turn-5", status: "completed", items: [{ id: "item:final-answer" }],
    });
  });

  it("改名通知直接更新内存与 SQLite 缓存，不依赖目录刷新", async () => {
    const profileId = "profile-title";
    const threadId = "thread-title";
    const client = new FakeOfficialClient();
    client.resume = async () => ({ thread: thread(threadId, []), page: page([], null) });
    client.listPage = async () => page([], null);
    client.metadata = async () => thread(threadId, []);
    installState(profileId, summaryRecord(threadId), client);
    await useAppStore.getState().loadThread(threadId);
    harness.saved = [];

    client.emitName(threadId, "Luna 生成标题");
    await vi.waitFor(() => expect(currentRecord(threadId).thread.name).toBe("Luna 生成标题"));

    expect(harness.saved).toHaveLength(1);
    expect((harness.saved[0] as ThreadRecord).thread.name).toBe("Luna 生成标题");
  });
});

function installState(profileId: string, record: ThreadRecord,
  client: FakeOfficialClient): void {
  harness.client = client;
  const connection: Connection = { kind: "ssh", profileId, name: profileId, active: true,
    host: "worker", port: 2222, user: "codex", keyRef: "key", hostFingerprint: null };
  useAppStore.setState({ activeConnection: connection, connections: [connection], threads: [record],
    projects: [], pendingRequests: {}, modelsByTarget: {}, error: null });
}

function currentRecord(threadId: string): ThreadRecord {
  return useAppStore.getState().threads.find((record) => record.thread.id === threadId)!;
}

function summaryRecord(threadId: string): ThreadRecord {
  return { thread: thread(threadId, []), archived: false, workspaceId: null,
    projectId: null, history: { kind: "summary" } };
}

function loadedRecord(threadId: string, value: Turn[], olderCursor: string | null): ThreadRecord {
  return { ...summaryRecord(threadId), thread: thread(threadId, value),
    history: { kind: "loaded", olderCursor, tailOlderCursor: olderCursor,
      hasLoadedOldest: olderCursor === null } };
}

function page(value: Turn[], nextCursor: string | null): OfficialTurnPage {
  return { turns: value, nextCursor, backwardsCursor: null };
}

function turns(first: number, last: number): Turn[] {
  return Array.from({ length: last - first + 1 }, (_, index) => turn(first + index));
}

function turn(index: number, status: Turn["status"] = "completed", marker = String(index)): Turn {
  return { id: `turn-${index}`, status, items: [{ type: "agentMessage", id: `item:${marker}`,
    text: marker, phase: "final_answer", memoryCitation: null }], itemsView: "full", error: null,
  startedAt: index, completedAt: status === "inProgress" ? null : index, durationMs: null };
}

function thread(id: string, value: Turn[]): Thread {
  return { id, sessionId: id, forkedFromId: null, parentThreadId: null, preview: id,
    ephemeral: false, section: null, sectionEnteredAt: null, modelProvider: "openai", createdAt: 1,
    updatedAt: 2, recencyAt: 2, status: { type: "idle" }, path: null, cwd: "/workspace",
    cliVersion: "0.147.0", source: "appServer", threadSource: null, agentNickname: null,
    agentRole: null, gitInfo: null, name: null, turns: value, extra: null, historyMode: "legacy",
    canAcceptDirectInput: true };
}

function deferred<Value>(): { promise: Promise<Value>; resolve: (value: Value) => void } {
  let resolve!: (value: Value) => void;
  return { promise: new Promise<Value>((done) => { resolve = done; }), resolve };
}
