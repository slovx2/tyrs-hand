import type { Turn } from "@codex-app-server/v2/Turn";
import { describe, expect, it } from "vitest";

import type { OfficialTurnPage } from "@/app-server/officialClient";
import { mergeOlderPage, mergeTailPage,
  mergeItemSnapshot, mergeTurnSequence, mergeTurnSnapshot } from "./threadHistory";

describe("官方 Turn 分页合并", () => {
  it("按 Turn ID 去重，并用最新快照原位替换活动 Turn", () => {
    const oldActive = turn("turn-3", "inProgress", "old");
    const newActive = turn("turn-3", "completed", "new");

    const merged = mergeTailPage([turn("turn-1"), turn("turn-2"), oldActive],
      [newActive, turn("turn-4")]);

    expect(merged.turns.map((item) => item.id)).toEqual(
      ["turn-1", "turn-2", "turn-3", "turn-4"]);
    expect(merged.turns[2]).toEqual(newActive);
    expect(merged.overlapped).toBe(true);
  });

  it("最新页没有重叠时丢弃有缺口的旧片段，由新游标重新分页", () => {
    const merged = mergeTailPage(
      [turn("turn-1"), turn("turn-2")],
      [turn("turn-8"), turn("turn-9")],
    );
    expect(merged).toEqual({
      turns: [turn("turn-8"), turn("turn-9")], overlapped: false,
    });
  });

  it("旧页重复 Turn 去重，过期游标响应不得覆盖新状态", () => {
    const current = [turn("turn-3"), turn("turn-4")];
    const merged = mergeOlderPage(current, "cursor-old", "cursor-old",
      page([turn("turn-1"), turn("turn-2"), turn("turn-3", "completed", "fresh")], null));
    expect(merged?.turns.map((item) => item.id)).toEqual([
      "turn-1", "turn-2", "turn-3", "turn-4",
    ]);
    expect(merged?.hasLoadedOldest).toBe(true);
    expect(mergeOlderPage(current, "cursor-new", "cursor-old",
      page([turn("turn-1")], null))).toBeNull();
  });

  it("多组重复页只保留第一次出现的位置和最后一次内容", () => {
    const latest = turn("turn-2", "completed", "latest");
    expect(mergeTurnSequence([turn("turn-1"), turn("turn-2")],
      [latest, turn("turn-3")])).toEqual([turn("turn-1"), latest, turn("turn-3")]);
  });

  it("内容未变化的官方 Turn 保留对象引用，避免无效重渲染", () => {
    const existing = turn("turn-1", "completed", "same");
    const incoming = turn("turn-1", "completed", "same");
    const existingTurns = [existing];

    const merged = mergeTailPage(existingTurns, [incoming]);

    expect(merged.turns[0]).toBe(existing);
    expect(merged.turns).toBe(existingTurns);
  });

  it("完成快照暂缺工具 Item 时保留已观察到的调用并更新终态", () => {
    const running = turn("turn-tool", "inProgress", "streaming");
    running.items.splice(1, 0, command("tool-1", "inProgress"));
    const completed = turn("turn-tool", "completed", "final");

    const merged = mergeTurnSnapshot(running, completed);

    expect(merged.status).toBe("completed");
    expect(merged.items.map((item) => item.id)).toEqual([
      "tool-1", "item:final",
    ]);
    expect(merged.items[0]).toMatchObject({ status: "completed" });
  });

  it("尾页从早期 Turn 重叠时仍合并最新 Turn 的原生工具", () => {
    const running = turn("turn-3", "inProgress", "streaming");
    running.items.splice(1, 0, command("native-tool", "completed"));
    const incoming = turn("turn-3", "completed", "final");

    const merged = mergeTailPage(
      [turn("turn-1"), turn("turn-2"), running],
      [turn("turn-1"), turn("turn-2"), incoming],
    );

    expect(merged.turns.at(-1)).toMatchObject({ status: "completed" });
    expect(merged.turns.at(-1)?.items.map((item) => item.id))
      .toEqual(["native-tool", "item:final"]);
  });

  it("同 ID 工具 Item 使用最新状态，新工具追加且不会重复", () => {
    const previous = turn("turn-tools", "inProgress", "streaming");
    previous.items.push(command("tool-1", "inProgress"));
    const incoming = turn("turn-tools", "completed", "final");
    incoming.items.unshift(command("tool-1", "completed"), command("tool-2", "completed"));

    const merged = mergeTurnSnapshot(previous, incoming);

    expect(merged.items.map((item) => item.id)).toEqual([
      "tool-1", "tool-2", "item:final",
    ]);
    expect(merged.items.find((item) => item.id === "tool-1")).toMatchObject({
      status: "completed",
    });
  });

  it("活动快照的短文本不能截断已揭示前缀，完成 Item 使用权威正文", () => {
    const streamed = { type: "agentMessage", id: "answer", text: "已经揭示的流式文本",
      phase: "final_answer", memoryCitation: null } as const;
    const stale = { ...streamed, text: "已经揭示" };
    const final = { ...streamed, text: "最终权威正文" };

    expect(mergeItemSnapshot(streamed, stale, false)).toBe(streamed);
    expect(mergeItemSnapshot(streamed, final, true)).toBe(final);
  });

  it("活动快照暂缺流式 Item 时保留已揭示内容", () => {
    const previous = turn("turn-stream", "inProgress", "streamed");
    const incoming = { ...turn("turn-stream", "inProgress", "stale"), items: [] };

    expect(mergeTurnSnapshot(previous, incoming).items).toEqual(previous.items);
  });

  it("legacy 尾页的合成 ID 与原生 Item 按语义一对一合并", () => {
    const previous = turn("turn-legacy", "inProgress", "unused");
    previous.items = [
      user("native-user", "message-id", "执行检查"),
      agent("native-commentary", "开始检查", "commentary"),
      command("native-tool", "inProgress"),
    ];
    const incoming = turn("turn-legacy", "inProgress", "unused");
    incoming.items = [
      user("item-1", "message-id", "执行检查"),
      agent("item-2", "开始检查并继续", "commentary"),
      { ...command("item-3", "completed"), id: "item-3" },
    ];

    const merged = mergeTurnSnapshot(previous, incoming);

    expect(merged.items.map((item) => item.id)).toEqual([
      "native-user", "native-commentary", "native-tool",
    ]);
    expect(merged.items[1]).toMatchObject({ text: "开始检查并继续" });
    expect(merged.items[2]).toMatchObject({ status: "completed" });
  });

  it("legacy 首条 User Item 缺少 clientId 时按完整输入去重", () => {
    const previous = turn("turn-legacy-user", "completed", "unused");
    previous.items = [user("native-user", null, "首条消息"),
      agent("answer", "回答", "final_answer")];
    const incoming = turn("turn-legacy-user", "completed", "unused");
    incoming.items = [user("item-0", null, "首条消息"),
      agent("item-1", "回答", "final_answer")];

    expect(mergeTurnSnapshot(previous, incoming).items.map((item) => item.id))
      .toEqual(["native-user", "answer"]);
  });

  it("重复执行相同命令时仍按出现顺序保留两次调用", () => {
    const previous = turn("turn-repeat", "inProgress", "unused");
    previous.items = [command("native-command-1", "completed")];
    const incoming = turn("turn-repeat", "inProgress", "unused");
    incoming.items = [command("item-1", "completed"), command("item-2", "inProgress")];

    const merged = mergeTurnSnapshot(previous, incoming);

    expect(merged.items.map((item) => item.id)).toEqual(["native-command-1", "item-2"]);
  });
});

function page(turns: Turn[], nextCursor: string | null): OfficialTurnPage {
  return { turns, nextCursor, backwardsCursor: null };
}

function turn(id: string, status: Turn["status"] = "completed", marker = id): Turn {
  return { id, status, items: [{ type: "agentMessage", id: `item:${marker}`,
    text: marker, phase: "final_answer", memoryCitation: null }], itemsView: "full",
  error: null, startedAt: 1, completedAt: status === "inProgress" ? null : 2, durationMs: null };
}

function command(id: string, status: "inProgress" | "completed"): Turn["items"][number] {
  return { type: "commandExecution", id, command: "git status", cwd: "/workspace",
    processId: null, source: "agent", status, commandActions: [], aggregatedOutput: null,
    exitCode: status === "completed" ? 0 : null, durationMs: null, pluginId: null,
    scriptPath: null };
}

function user(id: string, clientId: string | null, text: string): Turn["items"][number] {
  return { type: "userMessage", id, clientId,
    content: [{ type: "text", text, text_elements: [] }] };
}

function agent(id: string, text: string, phase: "commentary" | "final_answer"):
  Turn["items"][number] {
  return { type: "agentMessage", id, text, phase, memoryCitation: null };
}
