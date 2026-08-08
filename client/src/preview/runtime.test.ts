import type { ThreadListResponse } from "@codex-app-server/v2/ThreadListResponse";
import type { ThreadReadResponse } from "@codex-app-server/v2/ThreadReadResponse";
import { beforeEach, describe, expect, it } from "vitest";

import { CodexJsonRpcClient } from "@/app-server/jsonRpc";
import { primaryPreviewServerId, secondaryPreviewServerId } from "./config";
import { previewSessionIds } from "./fixtures";
import {
  createPreviewAppServerSocket,
  listPreviewConnections,
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

    expect(list.data).toHaveLength(5);
    expect(list.data.every((thread) => thread.turns.length === 0)).toBe(true);

    const expected = [
      [previewSessionIds.running, "inProgress", "agentMessage"],
      [previewSessionIds.plan, "completed", "plan"],
      [previewSessionIds.interactive, "inProgress", "userMessage"],
      [previewSessionIds.failed, "failed", "userMessage"],
    ] as const;
    for (const [threadId, status, itemType] of expected) {
      const detail = await client.request<ThreadReadResponse>("thread/read", {
        threadId,
        includeTurns: true,
      });
      const latestTurn = detail.thread.turns.at(-1);
      expect(latestTurn?.status).toBe(status);
      expect(latestTurn?.items.at(-1)?.type).toBe(itemType);
    }

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
      includeTurns: true,
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
    expect(primaryList.data).toHaveLength(6);
    expect(secondaryList.data).toHaveLength(1);
    expect(secondaryList.data[0]?.name).toBe("另一连接中的独立会话");
  });
});
