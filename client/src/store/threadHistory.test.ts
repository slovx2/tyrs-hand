import type { Turn } from "@codex-app-server/v2/Turn";
import { describe, expect, it } from "vitest";

import type { OfficialTurnPage } from "@/app-server/officialClient";
import { mergeOlderPage, mergeTailPage,
  mergeTurnSequence, mergeTurnSnapshot } from "./threadHistory";

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
