import { beforeEach, describe, expect, it, vi } from "vitest";

import type { MobileProject } from "@/app-server/types";
import { loadCachedProjects } from "@/db/cache";
import { listConnections, type Connection } from "@/db/connections";
import { loadSelectedProjectId, saveSelectedProjectId } from "@/db/settings";
import { useAppStore } from "./appStore";
import { officialClientFor } from "@/app-server/registry";
import { createPreviewSeed } from "@/preview/fixtures";
import { primaryPreviewServerId } from "@/preview/config";
import type { OfficialAppServerClient, OfficialItemPage } from "@/app-server/officialClient";
import { removePendingMessagePreview } from "@/db/pendingMessages";

vi.mock("expo-crypto", () => ({ randomUUID: vi.fn() }));
vi.mock("@/app-server/registry", () => ({ officialClientFor: vi.fn() }));
vi.mock("@/app-server/attachments", () => ({ materializeUserInput: vi.fn() }));
vi.mock("@/db/database", () => ({ getDatabase: vi.fn(), runDatabaseWrite: vi.fn(),
  withDatabaseTransaction: vi.fn() }));
vi.mock("@/db/cache", () => ({ loadCachedProjects: vi.fn(async () => []),
  loadCachedThreads: vi.fn(async () => []) }));
vi.mock("@/db/connections", () => ({ listConnections: vi.fn(async () => []),
  setActiveConnection: vi.fn(async () => undefined) }));
vi.mock("@/db/settings", () => ({ loadThemeMode: vi.fn(async () => "system"),
  loadSelectedProjectId: vi.fn(async () => null),
  saveSelectedProjectId: vi.fn(async () => undefined) }));
vi.mock("@/app-server/outbox", () => ({ listOutbox: vi.fn(async () => []) }));
vi.mock("@/db/pendingMessages", () => ({ listPendingMessagePreviews: vi.fn(async () => []),
  removePendingMessagePreview: vi.fn(async () => undefined) }));
vi.mock("@/db/threadReads", () => ({ loadUnreadThreadIds: vi.fn(async () => []),
  removeThreadRead: vi.fn(async () => undefined) }));

beforeEach(() => {
  vi.resetAllMocks();
  // 这里验证本地选择恢复；后台网络刷新单独覆盖。
  useAppStore.setState({ ...useAppStore.getInitialState(), refresh: vi.fn(async () => undefined) }, true);
});

describe("会话导航状态", () => {
  it("后台 Codex 的同 ID 审批事件不能覆盖当前 Claude 的待回答请求", async () => {
    const thread = createPreviewSeed().controls[primaryPreviewServerId]!.threads[0]!;
    const record = { thread, workspaceId: null, projectId: null, archived: false,
      history: { kind: "summary" as const } };
    const request = { id: "same-request", method: "item/tool/requestUserInput" as const,
      params: { threadId: thread.id, turnId: "same-turn", itemId: "same-item", questions: [],
        isBlocking: true, autoResolutionMs: null } };
    let notify!: Parameters<OfficialAppServerClient["subscribe"]>[0];
    const pendingRequests = vi.fn(() => [request]);
    vi.mocked(officialClientFor).mockReturnValue({ connect: async () => undefined,
      onClose: vi.fn(), subscribe: (listener: typeof notify) => { notify = listener; },
      pendingRequests, listTurnItems: async () => ({ items: [], nextCursor: null }),
    } as unknown as OfficialAppServerClient);
    useAppStore.setState({ activeConnection: connection("codex-profile"), threads: [record] });
    await useAppStore.getState().loadTurnItems(thread.id, "turn", null, "asc");
    const claudeRequest = { ...request, params: { ...request.params, itemId: "claude-item" } };
    useAppStore.setState({ activeConnection: { ...connection("claude-profile"), engine: "claude-code" },
      pendingRequests: { [thread.id]: [claudeRequest] } });
    notify(request);
    expect(useAppStore.getState().pendingRequests[thread.id]).toEqual([claudeRequest]);
    // 切回原入口后事件应照常更新，不能通过屏蔽全部审批来实现隔离。
    useAppStore.setState({ activeConnection: connection("codex-profile") });
    notify({ method: "serverRequest/resolved", params: { threadId: thread.id, requestId: request.id } });
    expect(useAppStore.getState().pendingRequests[thread.id]).toEqual([request]);
  });

  it("旧入口迟到的归档结果不能归档另一引擎的同名会话", async () => {
    const pending = deferred<void>();
    const archive = vi.fn(() => pending.promise);
    vi.mocked(officialClientFor).mockReturnValue({ connect: async () => undefined,
      onClose: vi.fn(), subscribe: vi.fn(), archive } as unknown as OfficialAppServerClient);
    const thread = createPreviewSeed().controls[primaryPreviewServerId]!.threads[0]!;
    const record = { thread, workspaceId: null, projectId: null, archived: false,
      history: { kind: "summary" as const } };
    useAppStore.setState({ activeConnection: connection("archive-codex"), threads: [record] });
    const operation = useAppStore.getState().setThreadArchived(thread.id, true);
    await vi.waitFor(() => expect(archive).toHaveBeenCalled());
    useAppStore.setState({ activeConnection: { ...connection("archive-claude"), engine: "claude-code" },
      threads: [record], unreadThreadIds: { [thread.id]: true } });
    pending.resolve();
    await operation;
    expect(useAppStore.getState().threads[0]?.archived).toBe(false);
    expect(useAppStore.getState().unreadThreadIds[thread.id]).toBe(true);
    expect(useAppStore.getState().refresh).not.toHaveBeenCalled();
  });

  it("旧入口的消息确认不能移除另一引擎的同 ID 待确认消息", async () => {
    const pending = deferred<void>();
    vi.mocked(removePendingMessagePreview).mockReturnValue(pending.promise);
    useAppStore.setState({ activeConnection: connection("confirm-codex") });
    const operation = useAppStore.getState().confirmPendingMessage("same-message");
    const message = { profileId: "confirm-claude", clientMessageId: "same-message", threadId: "same-thread",
      projectId: "shared-project", text: "Claude 消息", attachments: [], createdAt: "now" };
    useAppStore.setState({ activeConnection: { ...connection("confirm-claude"), engine: "claude-code" },
      pendingMessages: [message] });
    pending.resolve();
    await operation;
    expect(removePendingMessagePreview).toHaveBeenCalledWith("confirm-codex", "same-message");
    expect(useAppStore.getState().pendingMessages).toEqual([message]);
  });

  it("详情请求按连接、会话、Turn 和游标去重，切换连接后丢弃返回页", async () => {
    const pending = deferred<OfficialItemPage>();
    const listTurnItems = vi.fn(() => pending.promise);
    vi.mocked(officialClientFor).mockReturnValue({ connect: async () => undefined,
      onClose: vi.fn(), subscribe: vi.fn(), listTurnItems } as unknown as OfficialAppServerClient);
    const thread = createPreviewSeed().controls[primaryPreviewServerId]!.threads[0]!;
    useAppStore.setState({ activeConnection: connection("detail-profile"),
      threads: [{ thread, workspaceId: null, projectId: null, archived: false, history: { kind: "summary" } }] });
    const first = useAppStore.getState().loadTurnItems(thread.id, "turn", null, "asc");
    const second = useAppStore.getState().loadTurnItems(thread.id, "turn", null, "asc");
    const results = Promise.allSettled([first, second]);
    await vi.waitFor(() => expect(listTurnItems).toHaveBeenCalledTimes(1));
    useAppStore.setState({ activeConnection: connection("other-profile") });
    pending.resolve({ items: [], nextCursor: null });
    expect((await results).map((result) => result.status)).toEqual(["rejected", "rejected"]);
  });
  it("冷启动恢复当前机器上次选择的项目", async () => {
    vi.mocked(listConnections).mockResolvedValue([connection("machine-a")]);
    vi.mocked(loadCachedProjects).mockResolvedValue([project("first"), project("remembered")]);
    vi.mocked(loadSelectedProjectId).mockResolvedValue("remembered");

    await useAppStore.getState().initialize();

    expect(useAppStore.getState().selectedProjectId).toBe("remembered");
    expect(loadSelectedProjectId).toHaveBeenCalledWith("machine-a");
    expect(saveSelectedProjectId).not.toHaveBeenCalled();
  });

  it("已删除项目回退到可用项目，并保存有效选择", async () => {
    vi.mocked(listConnections).mockResolvedValue([connection("machine-a")]);
    vi.mocked(loadCachedProjects).mockResolvedValue([project("available")]);
    vi.mocked(loadSelectedProjectId).mockResolvedValue("deleted");

    await useAppStore.getState().initialize();

    expect(useAppStore.getState().selectedProjectId).toBe("available");
    expect(saveSelectedProjectId).toHaveBeenCalledWith("machine-a", "available");
  });

  it("后发的机器切换完成后，旧机器迟到的缓存不能覆盖新选择", async () => {
    const slowProjects = deferred<MobileProject[]>();
    vi.mocked(listConnections).mockResolvedValue([connection("slow"), connection("fast")]);
    vi.mocked(loadCachedProjects).mockImplementation(async (profileId) =>
      profileId === "slow" ? slowProjects.promise : [project("fast-project")]);
    vi.mocked(loadSelectedProjectId).mockImplementation(async (profileId) => `${profileId}-project`);

    const firstSwitch = useAppStore.getState().switchConnection("slow");
    await vi.waitFor(() => expect(loadCachedProjects).toHaveBeenCalledWith("slow"));
    await useAppStore.getState().switchConnection("fast");
    slowProjects.resolve([project("slow-project")]);
    await firstSwitch;

    expect(useAppStore.getState().activeConnection?.profileId).toBe("fast");
    expect(useAppStore.getState().selectedProjectId).toBe("fast-project");
    expect(useAppStore.getState().projects.map((item) => item.id)).toEqual(["fast-project"]);
    expect(useAppStore.getState().refresh).toHaveBeenCalledTimes(1);
  });

  it("手动选择只写入当前机器的项目偏好", () => {
    useAppStore.setState({ activeConnection: connection("machine-b") });
    useAppStore.getState().setSelectedProject("project-b");
    expect(useAppStore.getState().selectedProjectId).toBe("project-b");
    expect(saveSelectedProjectId).toHaveBeenCalledWith("machine-b", "project-b");
  });

});

function connection(profileId: string): Connection {
  return { kind: "ssh", engine: "codex", workerId: null, profileId, name: profileId, active: true,
    machineFingerprint: `test:${profileId}`, controls: [], host: "localhost", port: 22,
    user: "tester", keyRef: "test-key", hostFingerprint: null };
}

function project(id: string): MobileProject {
  return { id, name: id, workspaceId: null, relativePath: `/${id}`, cwd: `/${id}`,
    kind: "ssh", availabilityStatus: "available", branch: null, dirty: false };
}

function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void } {
  let resolve!: (value: T) => void;
  return { promise: new Promise<T>((done) => { resolve = done; }), resolve };
}
