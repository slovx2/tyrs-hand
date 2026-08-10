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
    readThreadMetadataIfExists: vi.fn(),
    findThreadBySource: vi.fn(),
    startThread: vi.fn(),
    submitNewThread: vi.fn(),
    submit: vi.fn(),
    readThreadMetadata: vi.fn(),
    listTurnPage: vi.fn(),
    pendingRequests: vi.fn(() => []),
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
  beforeEach(() => {
    vi.clearAllMocks();
    client.connect.mockResolvedValue(undefined);
    client.subscribe.mockReturnValue(() => undefined);
    client.findThreadBySource.mockResolvedValue(null);
    client.submitNewThread.mockResolvedValue({ threadId: "thread-new", turnId: "turn-1",
      deduplicated: false });
    client.submit.mockResolvedValue({ threadId: "thread-existing", turnId: "turn-1",
      deduplicated: false });
    client.readThreadMetadata.mockImplementation(async (threadId: string) => thread(threadId));
    client.listTurnPage.mockResolvedValue({ turns: [], nextCursor: null, backwardsCursor: null });
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
    client.readThreadMetadataIfExists.mockResolvedValue(null);
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
    client.readThreadMetadataIfExists.mockRejectedValue(new Error("network lost"));

    await useAppStore.getState().retryOutbox(messageId);

    expect(client.findThreadBySource).not.toHaveBeenCalled();
    expect(client.startThread).not.toHaveBeenCalled();
    expect(client.submitNewThread).not.toHaveBeenCalled();
  });
});

function activate(profileId: string): void {
  const connection: Connection = { profileId, kind: "ssh", name: "worker", active: true,
    host: "worker", port: 22, user: "tester", keyRef: "key", hostFingerprint: null };
  useAppStore.setState({ ready: true, refreshing: false, error: null,
    activeConnection: connection, connections: [connection], projects: [project], threads: [],
    outbox: [], selectedProjectId: project.id, modelsByTarget: {}, pendingRequests: {} });
}

function thread(id: string): Thread {
  return { id, sessionId: id, forkedFromId: null, parentThreadId: null, preview: "",
    ephemeral: false, section: null, sectionEnteredAt: null, modelProvider: "openai",
    createdAt: 1, updatedAt: 1, recencyAt: 1, status: { type: "idle" }, path: null,
    cwd: "/workspace", cliVersion: "0.147.0", source: "appServer", threadSource: null,
    agentNickname: null, agentRole: null, gitInfo: null, name: null, turns: [], extra: null,
    historyMode: "legacy", canAcceptDirectInput: true };
}
