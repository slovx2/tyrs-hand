import type { Thread } from "@codex-app-server/v2/Thread";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { enqueueOutbox, failOutbox, setOutboxThread } from "@/app-server/outbox";
import type { MobileProject } from "@/app-server/types";
import type { Connection } from "@/db/connections";
import { useAppStore } from "./appStore";

const { client } = vi.hoisted(() => ({
  client: {
    connect: vi.fn(async () => undefined),
    subscribe: vi.fn(() => () => undefined),
    onClose: vi.fn<(listener: (error: Error) => void) => () => void>(),
    readThreadMetadataIfExists: vi.fn(),
    resumeThreadForSubmissionIfExists: vi.fn(),
    findThreadBySource: vi.fn(),
    startThread: vi.fn(),
    submitNewThread: vi.fn(),
    submit: vi.fn(),
    readThreadMetadata: vi.fn(),
    listTurnPage: vi.fn(),
    listRecentThreads: vi.fn(async (): Promise<Thread[]> => []),
    pendingRequests: vi.fn(() => []),
    answerRequest: vi.fn(() => true),
    generateThreadTitle: vi.fn(async () => null),
  },
}));

vi.mock("expo-crypto", () => ({ randomUUID: () => "generated-message" }));
vi.mock("@/preview/config", () => ({ isPreviewMode: true, isPreviewServerId: () => false }));
vi.mock("@/db/database", () => ({ getDatabase: vi.fn(), runDatabaseWrite: vi.fn(),
  withDatabaseTransaction: vi.fn() }));
vi.mock("@/db/cache", () => ({ loadCachedProjects: vi.fn(async () => []),
  loadCachedThreads: vi.fn(async () => []), replaceCachedThreads: vi.fn(async () => undefined),
  saveProjects: vi.fn(async () => undefined), saveThreadRecord: vi.fn(async () => undefined) }));
vi.mock("@/db/connections", () => ({ listConnections: vi.fn(async () => []),
  setActiveConnection: vi.fn(async () => undefined) }));
vi.mock("@/db/settings", () => ({ loadThemeMode: vi.fn(async () => "system"),
  saveThemeMode: vi.fn(), saveLastTurnPreferences: vi.fn(async () => undefined) }));
vi.mock("@/db/sshProjects", () => ({ listSSHProjects: vi.fn(async () => []) }));
vi.mock("@/app-server/registry", () => ({ officialClientFor: () => client }));
vi.mock("@/app-server/attachments", () => ({ materializeUserInput: vi.fn(async (
  _connection: unknown, _project: unknown, _clientMessageId: string, text: string,
) => [{ type: "text", text, text_elements: [] }]) }));

const preferences = { model: "gpt-test", effort: "low" as const, serviceTier: null,
  collaborationMode: "default" as const };
const project: MobileProject = { id: "project-1", workspaceId: null, name: "workspace",
  relativePath: "/workspace", cwd: "/workspace", kind: "ssh",
  availabilityStatus: "available", branch: null, dirty: false };

describe("移动端 Outbox 新 Thread", () => {
  it("SSH 直连断开时只清理本地交互请求，不改 Thread 状态", async () => {
    const profileId = "ssh-close";
    let closeListener: ((error: Error) => void) | null = null;
    client.onClose.mockImplementation((listener: (error: Error) => void) => {
      closeListener = listener;
      return () => undefined;
    });
    activate(profileId, [{ serverId: "control-1", baseUrl: "https://control.example",
      workerId: "worker-1", workerName: "worker", deviceId: "device-1" }]);
    const started = thread("thread-close");
    client.startThread.mockResolvedValue({ thread: started });
    client.submitNewThread.mockResolvedValue({ threadId: started.id, turnId: "turn-1",
      deduplicated: false });

    await expect(useAppStore.getState().startTask(project.id, "触发交互", [], preferences,
      "message-close")).resolves.toBe(started.id);
    expect(closeListener).toEqual(expect.any(Function));
    useAppStore.setState({ threads: [{ thread: started, archived: false, workspaceId: null,
      projectId: project.id, history: { kind: "loaded", olderCursor: null,
        tailOlderCursor: null, hasLoadedOldest: true } }],
    pendingRequests: { [started.id]: [{ id: "request-1", method: "item/tool/requestUserInput",
      params: { threadId: started.id, turnId: "turn-1", itemId: "item-1", questions: [],
        isBlocking: true, autoResolutionMs: null } }] } });

    (closeListener as unknown as (error: Error) => void)(new Error("network lost"));

    expect(useAppStore.getState().pendingRequests).toEqual({});
    expect(useAppStore.getState().threads[0]?.thread.id).toBe(started.id);
    expect(useAppStore.getState().error).toBe("SSH App Server 连接已断开，请重试");
  });

  beforeEach(() => {
    vi.clearAllMocks();
    client.connect.mockResolvedValue(undefined);
    client.subscribe.mockReturnValue(() => undefined);
    client.onClose.mockReturnValue(() => undefined);
    client.findThreadBySource.mockResolvedValue(null);
    client.submitNewThread.mockResolvedValue({ threadId: "thread-new", turnId: "turn-1",
      deduplicated: false });
    client.submit.mockResolvedValue({ threadId: "thread-existing", turnId: "turn-1",
      deduplicated: false });
    client.readThreadMetadata.mockImplementation(async (threadId: string) => thread(threadId));
    client.listTurnPage.mockResolvedValue({ turns: [], nextCursor: null, backwardsCursor: null });
    client.listRecentThreads.mockResolvedValue([]);
    client.answerRequest.mockReturnValue(true);
    client.generateThreadTitle.mockResolvedValue(null);
  });

  it("首次提交直接复用 thread/start 返回的内存 Thread", async () => {
    const profileId = "outbox-first-submit";
    activate(profileId);
    const started = thread("thread-new");
    client.startThread.mockResolvedValue({ thread: started });

    await expect(useAppStore.getState().startTask(project.id, "打开网页", [], preferences,
      "message-first")).resolves.toBe(started.id);

    expect(client.submitNewThread).toHaveBeenCalledWith(started, expect.objectContaining({
      clientMessageId: "message-first",
    }));
    expect(client.submit).not.toHaveBeenCalled();
    expect(useAppStore.getState().threads.find((record) => record.thread.id === started.id)
      ?.preferences).toEqual(preferences);
  });

  it("已持久化但未物化的 phantom Thread 按稳定 source 重建", async () => {
    const profileId = "outbox-phantom-recovery";
    const messageId = "message-phantom";
    activate(profileId);
    await enqueueOutbox({ profileId, clientMessageId: messageId, kind: "create_task",
      projectId: project.id, threadId: null,
      payload: { text: "打开网页", attachments: [], preferences } });
    await setOutboxThread(profileId, messageId, "thread-phantom");
    await failOutbox(profileId, messageId, "previous crash");
    client.resumeThreadForSubmissionIfExists.mockResolvedValue(null);
    client.findThreadBySource.mockResolvedValue(thread("thread-phantom"));
    const replacement = thread("thread-replacement");
    client.startThread.mockResolvedValue({ thread: replacement });

    await useAppStore.getState().retryOutbox(messageId);

    expect(client.findThreadBySource).toHaveBeenCalledWith(
      `tyrs-hand-mobile:${profileId}:${messageId}`);
    expect(client.submitNewThread).toHaveBeenCalledWith(replacement,
      expect.objectContaining({ clientMessageId: messageId }));
    expect(client.submit).not.toHaveBeenCalled();
  });

  it("未知读取错误不会触发第二个 Thread", async () => {
    const profileId = "outbox-unknown-error";
    const messageId = "message-unknown";
    activate(profileId);
    await enqueueOutbox({ profileId, clientMessageId: messageId, kind: "create_task",
      projectId: project.id, threadId: null,
      payload: { text: "打开网页", attachments: [], preferences } });
    await setOutboxThread(profileId, messageId, "thread-unknown");
    await failOutbox(profileId, messageId, "previous crash");
    client.resumeThreadForSubmissionIfExists.mockRejectedValue(new Error("network lost"));

    await useAppStore.getState().retryOutbox(messageId);

    expect(client.findThreadBySource).not.toHaveBeenCalled();
    expect(client.startThread).not.toHaveBeenCalled();
    expect(client.submitNewThread).not.toHaveBeenCalled();
  });

  it("现有 Thread 提交等待网络期间不插入乐观 Turn，成功后创建预显示贴片", async () => {
    const profileId = "outbox-optimistic";
    const threadId = "thread-existing";
    activate(profileId);
    useAppStore.setState({ threads: [{ thread: thread(threadId), archived: false,
      workspaceId: null, projectId: project.id,
      history: { kind: "loaded", olderCursor: null, tailOlderCursor: null,
        hasLoadedOldest: true } }] });
    const pending = deferred<{ threadId: string; turnId: string; deduplicated: boolean }>();
    client.submit.mockReturnValueOnce(pending.promise);

    const submission = useAppStore.getState().submitMessage(threadId, "立即显示", [], preferences,
      "optimistic-message");
    expect(useAppStore.getState().threads[0]?.thread.turns).toHaveLength(0);
    expect(useAppStore.getState().pendingMessages).toHaveLength(0);

    pending.resolve({ threadId, turnId: "turn-1", deduplicated: false });
    await expect(submission).resolves.toBe(true);
    expect(useAppStore.getState().pendingMessages[0]).toMatchObject({
      clientMessageId: "optimistic-message", threadId, text: "立即显示",
    });
  });

  it("执行计划不插入乐观 Turn 但向 App Server 保留完整计划", async () => {
    const profileId = "execute-plan-optimistic";
    const threadId = "thread-plan";
    activate(profileId);
    const planned = thread(threadId);
    planned.turns = [{ id: "turn-plan", status: "completed", error: null, startedAt: 1,
      completedAt: 2, durationMs: 1, itemsView: "full",
      items: [{ type: "plan", id: "plan-item", text: "# 计划\n- 修改代码" }] }];
    useAppStore.setState({ threads: [{ thread: planned, archived: false, workspaceId: null,
      projectId: project.id, history: { kind: "loaded", olderCursor: null,
        tailOlderCursor: null, hasLoadedOldest: true } }] });
    const pending = deferred<{ threadId: string; turnId: string; deduplicated: boolean }>();
    client.submit.mockReturnValueOnce(pending.promise);

    const submission = useAppStore.getState().executePlan(threadId, preferences);
    expect(useAppStore.getState().threads[0]?.thread.turns).toHaveLength(1);
    await vi.waitFor(() => expect(client.submit).toHaveBeenCalledWith(expect.objectContaining({
      clientMessageId: "plan:thread-plan:plan-item",
      input: [{ type: "text", text: "PLEASE IMPLEMENT THIS PLAN:\n# 计划\n- 修改代码",
        text_elements: [] }],
    })));

    pending.resolve({ threadId, turnId: "turn-implementation", deduplicated: false });
    await expect(submission).resolves.toBeUndefined();
  });

  it("回答 requestUserInput 后立即插入可持久化的本地回显 Item", () => {
    const profileId = "answer-user-input";
    const threadId = "thread-question";
    activate(profileId);
    const current = thread(threadId);
    current.status = { type: "active", activeFlags: [] };
    current.turns = [{ id: "turn-question", status: "inProgress", error: null,
      startedAt: 1, completedAt: null, durationMs: null, itemsView: "full",
      items: [{ type: "userMessage", id: "user-question", clientId: "message-question",
        content: [{ type: "text", text: "执行", text_elements: [] }] }] }];
    const request = { id: "request-1", method: "item/tool/requestUserInput" as const,
      params: { threadId, turnId: "turn-question", itemId: "question-item",
        questions: [{ id: "choice", header: "方式", question: "继续吗？", isOther: false,
          isSecret: false, options: [{ label: "继续", description: "继续执行" }] }],
        isBlocking: true, autoResolutionMs: null } };
    useAppStore.setState({ threads: [{ thread: current, archived: false, workspaceId: null,
      projectId: project.id, history: { kind: "loaded", olderCursor: null,
        tailOlderCursor: null, hasLoadedOldest: true } }],
    pendingRequests: { [threadId]: [request] } });

    expect(useAppStore.getState().answerRequest(threadId, request.id,
      { answers: { choice: { answers: ["继续"] } } })).toBe(true);

    expect(current.turns[0]?.items).toHaveLength(1);
    expect(useAppStore.getState().threads[0]?.thread.turns[0]?.items.at(-1)).toEqual({
      type: "userInputResponse",
      id: "user-input-response-request-1",
      requestId: "request-1",
      turnId: "turn-question",
      questions: [{ id: "choice", header: "方式", question: "继续吗？",
        options: [{ label: "继续", description: "继续执行" }] }],
      answers: { choice: ["继续"] },
      completed: true,
    });
    expect(useAppStore.getState().pendingRequests[threadId]).toEqual([]);
  });

  it("应用级同步会在详情页之外刷新所有活动 Thread", async () => {
    const profileId = "foreground-active-sync";
    const threadId = "thread-active";
    activate(profileId);
    const active = thread(threadId);
    active.status = { type: "active", activeFlags: [] };
    useAppStore.setState({ threads: [{ thread: active, archived: false, workspaceId: null,
      projectId: project.id, history: { kind: "loaded", olderCursor: null,
        tailOlderCursor: null, hasLoadedOldest: true } }] });

    await useAppStore.getState().refreshActiveThreads();

    expect(client.readThreadMetadata).toHaveBeenCalledWith(threadId);
    expect(client.listTurnPage).toHaveBeenCalledWith(threadId, null, 5, "full", "legacy");
  });

  it("轻量近期目录会发现新活动 Thread 并立即读取尾页", async () => {
    const profileId = "foreground-recent-sync";
    activate(profileId);
    const discovered = thread("thread-discovered");
    discovered.status = { type: "active", activeFlags: [] };
    discovered.updatedAt = 10;
    discovered.recencyAt = 10;
    client.listRecentThreads.mockResolvedValueOnce([discovered]);
    client.readThreadMetadata.mockResolvedValueOnce(discovered);

    await useAppStore.getState().refreshRecentThreads();

    expect(client.listRecentThreads).toHaveBeenCalledWith();
    expect(client.readThreadMetadata).toHaveBeenCalledWith(discovered.id);
    expect(useAppStore.getState().threads.some((record) =>
      record.thread.id === discovered.id)).toBe(true);
  });
});

function activate(profileId: string, controls: Connection["controls"] = []): void {
  const connection: Connection = { profileId, kind: "ssh", name: "worker", active: true,
    machineFingerprint: `test:${profileId}`, controls, host: "worker", port: 22,
    user: "tester", keyRef: "key", hostFingerprint: null };
  useAppStore.setState({ ready: true, refreshing: false, error: null,
    activeConnection: connection, connections: [connection], projects: [project], threads: [],
    outbox: [], pendingMessages: [], unreadThreadIds: {}, selectedProjectId: project.id,
    modelsByTarget: {}, pendingRequests: {} });
}

function deferred<Value>(): { promise: Promise<Value>; resolve: (value: Value) => void } {
  let resolve!: (value: Value) => void;
  return { promise: new Promise<Value>((done) => { resolve = done; }), resolve };
}

function thread(id: string): Thread {
  return { id, sessionId: id, forkedFromId: null, parentThreadId: null, preview: "",
    ephemeral: false, section: null, sectionEnteredAt: null, modelProvider: "openai",
    createdAt: 1, updatedAt: 1, recencyAt: 1, status: { type: "idle" }, path: null,
    cwd: "/workspace", cliVersion: "0.147.0", source: "appServer", threadSource: null,
    agentNickname: null, agentRole: null, gitInfo: null, name: null, turns: [], extra: null,
    historyMode: "legacy", canAcceptDirectInput: true };
}
