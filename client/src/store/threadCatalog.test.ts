import type { Thread } from "@codex-app-server/v2/Thread";
import { describe, expect, it } from "vitest";

import type { ThreadRecord } from "@/app-server/types";
import { mergeThreadCatalog } from "./threadCatalog";

describe("官方会话目录合并", () => {
  it("thread/list 尚未收录本地新会话时保留乐观记录", () => {
    const pending = record("pending", 3, "loaded");
    const merged = mergeThreadCatalog([record("listed", 2)], [pending], new Set(["pending"]));
    expect(merged.map((item) => item.thread.id)).toEqual(["pending", "listed"]);
  });

  it("服务端收录后使用官方元数据并保留已加载历史", () => {
    const loaded = record("pending", 1, "loaded");
    loaded.preferences = { model: "gpt-5.6-terra", effort: "high", serviceTier: "priority",
      collaborationMode: "default" };
    const summary = record("pending", 4, "summary");
    summary.thread.name = "Luna 标题";
    const merged = mergeThreadCatalog([summary], [loaded], new Set(["pending"]));
    expect(merged).toHaveLength(1);
    expect(merged[0]?.thread.name).toBe("Luna 标题");
    expect(merged[0]?.thread.turns).toBe(loaded.thread.turns);
    expect(merged[0]?.history.kind).toBe("loaded");
    expect(merged[0]?.preferences?.model).toBe("gpt-5.6-terra");
  });
});

function record(id: string, updatedAt: number,
  history: "loaded" | "summary" = "summary"): ThreadRecord {
  const thread: Thread = { id, sessionId: id, forkedFromId: null, parentThreadId: null,
    preview: id, ephemeral: false, section: null, sectionEnteredAt: null, historyMode: "legacy",
    modelProvider: "openai", createdAt: 1, updatedAt, recencyAt: updatedAt,
    status: { type: "idle" }, path: null, cwd: "/workspace", cliVersion: "0.147.0",
    source: "appServer", canAcceptDirectInput: true, threadSource: null, agentNickname: null,
    agentRole: null, gitInfo: null, name: null, turns: [], extra: null };
  return { thread, archived: false, workspaceId: null, projectId: null,
    history: history === "loaded" ? { kind: "loaded", olderCursor: null,
      tailOlderCursor: null, hasLoadedOldest: true } : { kind: "summary" } };
}
