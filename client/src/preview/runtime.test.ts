import type { ThreadListResponse } from "@codex-app-server/v2/ThreadListResponse";
import type { ThreadReadResponse } from "@codex-app-server/v2/ThreadReadResponse";
import type { ThreadResumeResponse } from "@codex-app-server/v2/ThreadResumeResponse";
import type { ThreadTurnsListResponse } from "@codex-app-server/v2/ThreadTurnsListResponse";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { CodexJsonRpcClient } from "@/app-server/jsonRpc";
import { primaryPreviewServerId, secondaryPreviewServerId } from "./config";
import { previewSessionIds } from "./fixtures";
import {
  createPreviewAppServerSocket,
  listPreviewConnections,
  previewActivityTimelineMs,
  resetPreviewState,
  setPreviewActiveConnection,
} from "./runtime";

async function previewClient(serverId: string): Promise<CodexJsonRpcClient> {
  const client = new CodexJsonRpcClient(() => createPreviewAppServerSocket(serverId), 1_000);
  await client.open();
  return client;
}

describe("官方协议预览模式", () => {
  beforeEach(() => resetPreviewState());

  it("通过官方 Thread -> Turn -> Item 提供全部关键 UI 状态", async () => {
    const client = await previewClient(primaryPreviewServerId);
    const list = await client.request<ThreadListResponse>("thread/list", {
      cursor: null,
      limit: 100,
      sortKey: "updated_at",
      sortDirection: "desc",
      archived: false,
    });

    expect(list.data).toHaveLength(6);
    expect(list.data.every((thread) => thread.turns.length === 0)).toBe(true);

    const expected = [
      [previewSessionIds.running, "inProgress", "commandExecution"],
      [previewSessionIds.plan, "completed", "plan"],
      [previewSessionIds.interactive, "inProgress", "userMessage"],
      [previewSessionIds.failed, "failed", "userMessage"],
    ] as const;
    for (const [threadId, status, itemType] of expected) {
      const detail = await client.request<ThreadResumeResponse>("thread/resume", {
        threadId, excludeTurns: true,
        initialTurnsPage: { limit: 5, sortDirection: "desc", itemsView: "full" },
      });
      expect(detail.thread.turns).toEqual([]);
      const latestTurn = detail.initialTurnsPage?.data[0];
      expect(latestTurn?.status).toBe(status);
      expect(latestTurn?.items.at(-1)?.type).toBe(itemType);
    }

    const longTurns: ThreadTurnsListResponse["data"] = [];
    let cursor: string | null = null;
    do {
      const page: ThreadTurnsListResponse = await client.request("thread/turns/list", {
        threadId: previewSessionIds.long, cursor, limit: 5,
        sortDirection: "desc", itemsView: "full",
      });
      longTurns.push(...page.data);
      cursor = page.nextCursor;
    } while (cursor);
    expect(longTurns).toHaveLength(32);
    expect(longTurns[0]?.items.some((item) => item.type === "agentMessage" &&
      item.text.includes("LONG_CONVERSATION_LATEST"))).toBe(true);
    expect(longTurns[0]?.items.some((item) => item.type === "agentMessage" &&
      item.phase === "commentary")).toBe(true);
    expect(longTurns[0]?.items.some((item) => item.type === "commandExecution")).toBe(true);

    const archived = await client.request<ThreadListResponse>("thread/list", {
      cursor: null,
      limit: 100,
      sortKey: "updated_at",
      sortDirection: "desc",
      archived: true,
    });
    expect(archived.data.map((thread) => thread.id)).toEqual([previewSessionIds.archived]);
  });

  it("保持 profile 隔离并只用官方写方法修改 Thread", async () => {
    const primary = await previewClient(primaryPreviewServerId);
    const secondary = await previewClient(secondaryPreviewServerId);
    setPreviewActiveConnection(secondaryPreviewServerId);
    expect(listPreviewConnections().find((item) => item.active)?.profileId)
      .toBe(secondaryPreviewServerId);

    await primary.request("thread/name/set", {
      threadId: previewSessionIds.running,
      name: "预览中修改后的标题",
    });
    await primary.request("thread/unarchive", { threadId: previewSessionIds.archived });

    const changed = await primary.request<ThreadReadResponse>("thread/read", {
      threadId: previewSessionIds.running,
      includeTurns: false,
    });
    expect(changed.thread.name).toBe("预览中修改后的标题");

    const primaryList = await primary.request<ThreadListResponse>("thread/list", {
      cursor: null,
      limit: 100,
      sortKey: "updated_at",
      sortDirection: "desc",
      archived: false,
    });
    const secondaryList = await secondary.request<ThreadListResponse>("thread/list", {
      cursor: null,
      limit: 100,
      sortKey: "updated_at",
      sortDirection: "desc",
      archived: false,
    });
    expect(primaryList.data).toHaveLength(7);
    expect(secondaryList.data).toHaveLength(1);
    expect(secondaryList.data[0]?.name).toBe("另一连接中的独立会话");
  });

  it("Luna 临时线程不进入目录，并可通过 unsubscribe 清理", async () => {
    const client = await previewClient(primaryPreviewServerId);
    const started = await client.request<{ thread: { id: string } }>("thread/start", {
      cwd: "/preview/workspaces/tyrs-hand", model: "gpt-5.6-luna", ephemeral: true,
      permissions: ":read-only", runtimeWorkspaceRoots: [],
    });
    const list = await client.request<ThreadListResponse>("thread/list", {
      cursor: null, limit: 100, sortKey: "updated_at", sortDirection: "desc", archived: false,
    });
    expect(list.data.some((thread) => thread.id === started.thread.id)).toBe(false);

    await client.request("thread/unsubscribe", { threadId: started.thread.id });
    await expect(client.request("thread/read", { threadId: started.thread.id,
      includeTurns: false })).rejects.toThrow();
  });

  it("Luna 标题 Turn 在完成通知前发送结构化 Agent Item", async () => {
    const client = await previewClient(primaryPreviewServerId);
    const notifications: { method: string; params: unknown }[] = [];
    client.onNotification((notification) => notifications.push(notification));
    const started = await client.request<{ thread: { id: string } }>("thread/start", {
      cwd: "/preview/workspaces/tyrs-hand", model: "gpt-5.6-luna", ephemeral: true,
      permissions: ":read-only", runtimeWorkspaceRoots: [],
    });
    await client.request("turn/start", { threadId: started.thread.id, input: [],
      outputSchema: { type: "object" } });
    await new Promise((resolve) => setTimeout(resolve, 100));

    const completedItem = notifications.find((item) => item.method === "item/completed");
    const completedTurn = notifications.find((item) => item.method === "turn/completed");
    expect(completedItem).toBeDefined();
    expect(completedTurn).toBeDefined();
    expect(notifications.indexOf(completedItem!)).toBeLessThan(notifications.indexOf(completedTurn!));
    expect(completedItem?.params).toMatchObject({ item: { type: "agentMessage",
      text: JSON.stringify({ title: "生成预览任务标题", description: "预览任务自动标题" }) } });
  });

  it("按工具完成、最终回答首段、Turn 完成三个阶段驱动动态预览", async () => {
    vi.useFakeTimers();
    try {
      const socket = createPreviewAppServerSocket(primaryPreviewServerId);
      const messages: Record<string, unknown>[] = [];
      socket.onmessage = (event) => messages.push(JSON.parse(String(event.data)) as Record<string, unknown>);
      socket.send(JSON.stringify({ id: 1, method: "thread/resume", params: {
        threadId: previewSessionIds.running, excludeTurns: true,
        initialTurnsPage: { limit: 5, sortDirection: "desc", itemsView: "full" },
      } }));
      await vi.advanceTimersByTimeAsync(previewActivityTimelineMs.toolCompleted);
      expect(messages.some((message) => message.method === "item/completed")).toBe(true);
      await vi.advanceTimersByTimeAsync(previewActivityTimelineMs.finalStarted -
        previewActivityTimelineMs.toolCompleted);
      expect(messages.some((message) => message.method === "item/agentMessage/delta")).toBe(true);
      await vi.advanceTimersByTimeAsync(previewActivityTimelineMs.turnCompleted -
        previewActivityTimelineMs.finalStarted);
      const completed = messages.find((message) => message.method === "turn/completed") as
        { params?: { turn?: { status?: string } } } | undefined;
      expect(completed?.params?.turn?.status).toBe("completed");
      socket.close();
    } finally {
      vi.useRealTimers();
    }
  });
});
