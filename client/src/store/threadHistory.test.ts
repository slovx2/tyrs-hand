import type { Turn } from "@codex-app-server/v2/Turn";
import { describe, expect, it } from "vitest";

import type { OfficialTurnPage } from "@/app-server/officialClient";
import { mergeOlderPage, mergeTailPage,
  mergeTurnSequence } from "./threadHistory";

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
});

function page(turns: Turn[], nextCursor: string | null): OfficialTurnPage {
  return { turns, nextCursor, backwardsCursor: null };
}

function turn(id: string, status: Turn["status"] = "completed", marker = id): Turn {
  return { id, status, items: [{ type: "agentMessage", id: `item:${marker}`,
    text: marker, phase: "final_answer", memoryCitation: null }], itemsView: "full",
  error: null, startedAt: 1, completedAt: status === "inProgress" ? null : 2, durationMs: null };
}
