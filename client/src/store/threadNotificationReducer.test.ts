import type { ServerNotification } from "@codex-app-server/ServerNotification";
import type { Thread } from "@codex-app-server/v2/Thread";
import type { ThreadItem } from "@codex-app-server/v2/ThreadItem";
import type { Turn } from "@codex-app-server/v2/Turn";
import { describe, expect, it } from "vitest";

import type { ThreadRecord } from "@/app-server/types";
import { reduceThreadNotification } from "./threadNotificationReducer";

describe("原生 Thread 通知 reducer", () => {
  it("正式 turn/started 按 clientId 替换乐观 Turn，不重复用户消息", () => {
    const provisional = turn("provisional:message", "inProgress", [user("p", "message")]);
    const official = turn("official", "inProgress", [user("u", "message")]);

    const result = reduceThreadNotification(record([provisional]),
      { method: "turn/started", params: { threadId: "thread", turn: official } });

    expect(result.record.thread.turns.map((value) => value.id)).toEqual(["official"]);
    expect(result.record.thread.status.type).toBe("active");
  });

  it("legacy turn/started 缺少 clientId 时按完整输入替换乐观 Turn", () => {
    const provisional = turn("provisional:message", "inProgress", [user("p", "message")]);
    const official = turn("official", "inProgress", [user("u", null)]);

    const result = reduceThreadNotification(record([provisional]),
      { method: "turn/started", params: { threadId: "thread", turn: official } });

    expect(result.record.thread.turns.map((value) => value.id)).toEqual(["official"]);
  });

  it("item 生命周期按 ID 稳定 upsert，并用完成 Item 更新工具状态", () => {
    const initial = record([turn("turn", "inProgress", [])]);
    const started = reduceThreadNotification(initial, itemEvent("item/started",
      command("tool", "inProgress")));
    const completed = reduceThreadNotification(started.record, itemEvent("item/completed",
      command("tool", "completed")));

    expect(started.record.thread.turns[0]?.items).toHaveLength(1);
    expect(completed.record.thread.turns[0]?.items[0]).toMatchObject({ id: "tool",
      status: "completed" });
    expect(completed.terminal).toBe(true);
  });

  it("文本 delta 只增长活动 Item，缺失父 Item 时请求权威对账", () => {
    const value = record([turn("turn", "inProgress", [agent("answer", "已有")])]);
    const updated = reduceThreadNotification(value, agentDelta("answer", "内容"));
    const missing = reduceThreadNotification(updated.record, agentDelta("missing", "丢失"));

    expect(updated.record.thread.turns[0]?.items[0]).toMatchObject({ text: "已有内容" });
    expect(missing.changed).toBe(false);
    expect(missing.needsRefresh).toBe(true);
  });

  it("reasoning summary 分段 delta 保持索引并逐步追加", () => {
    const reasoning: ThreadItem = { type: "reasoning", id: "reasoning", summary: [], content: [] };
    let current = record([turn("turn", "inProgress", [reasoning])]);
    const events: ServerNotification[] = [
      { method: "item/reasoning/summaryPartAdded", params: { threadId: "thread",
        turnId: "turn", itemId: "reasoning", summaryIndex: 1 } },
      { method: "item/reasoning/summaryTextDelta", params: { threadId: "thread",
        turnId: "turn", itemId: "reasoning", summaryIndex: 1, delta: "第二段" } },
    ];
    for (const event of events) current = reduceThreadNotification(current, event).record;

    expect(current.thread.turns[0]?.items[0]).toMatchObject({ summary: ["", "第二段"] });
  });

  it("turn/completed 应用权威正文，同时保留完成瞬间遗漏的既有工具", () => {
    const running = turn("turn", "inProgress", [command("tool", "inProgress"),
      agent("answer", "流式")]);
    const completed = turn("turn", "completed", [agent("answer", "最终回答")]);

    const result = reduceThreadNotification(record([running]),
      { method: "turn/completed", params: { threadId: "thread", turn: completed } });

    expect(result.record.thread.turns[0]).toMatchObject({ status: "completed" });
    expect(result.record.thread.turns[0]?.items.map((item) => item.id))
      .toEqual(["tool", "answer"]);
    expect(result.record.thread.turns[0]?.items[1]).toMatchObject({ text: "最终回答" });
  });

  it("turn/completed 的短快照不会让既有用户消息和 commentary 短暂消失", () => {
    const running = turn("turn", "inProgress", [user("user", "message"),
      agent("commentary", "处理中", "commentary"),
      command("tool", "completed")]);
    const completed = turn("turn", "completed", [agent("answer", "最终回答")]);

    const result = reduceThreadNotification(record([running]),
      { method: "turn/completed", params: { threadId: "thread", turn: completed } });

    expect(result.record.thread.turns[0]?.items.map((item) => item.id))
      .toEqual(["user", "commentary", "tool", "answer"]);
  });
});

function record(turns: Turn[]): ThreadRecord {
  const thread: Thread = { id: "thread", sessionId: "thread", forkedFromId: null,
    parentThreadId: null, preview: "thread", ephemeral: false, section: null,
    sectionEnteredAt: null, historyMode: "paginated", modelProvider: "openai", createdAt: 1,
    updatedAt: 2, recencyAt: 2, status: { type: "idle" }, path: null, cwd: "/workspace",
    cliVersion: "0.147.0", source: "appServer", canAcceptDirectInput: true,
    threadSource: null, agentNickname: null, agentRole: null, gitInfo: null, name: null,
    turns, extra: null };
  return { thread, archived: false, workspaceId: null, projectId: "project",
    history: { kind: "loaded", olderCursor: null, tailOlderCursor: null,
      hasLoadedOldest: true } };
}

function turn(id: string, status: Turn["status"], items: ThreadItem[]): Turn {
  return { id, status, items, itemsView: "full", error: null, startedAt: 1,
    completedAt: status === "inProgress" ? null : 2, durationMs: null };
}

function user(id: string, clientId: string | null): ThreadItem {
  return { type: "userMessage", id, clientId,
    content: [{ type: "text", text: "消息", text_elements: [] }] };
}

function agent(id: string, text: string,
  phase: "commentary" | "final_answer" = "final_answer"):
  Extract<ThreadItem, { type: "agentMessage" }> {
  return { type: "agentMessage", id, text, phase, memoryCitation: null };
}

function command(id: string, status: "inProgress" | "completed"): ThreadItem {
  return { type: "commandExecution", id, command: "pwd", cwd: "/workspace", processId: null,
    source: "agent", status, commandActions: [], aggregatedOutput: null,
    exitCode: status === "completed" ? 0 : null, durationMs: null, pluginId: null,
    scriptPath: null };
}

function itemEvent(method: "item/started" | "item/completed", item: ThreadItem): ServerNotification {
  return method === "item/started"
    ? { method, params: { threadId: "thread", turnId: "turn", item, startedAtMs: 1 } }
    : { method, params: { threadId: "thread", turnId: "turn", item, completedAtMs: 2 } };
}

function agentDelta(itemId: string, delta: string): ServerNotification {
  return { method: "item/agentMessage/delta",
    params: { threadId: "thread", turnId: "turn", itemId, delta } };
}
