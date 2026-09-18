import type { ServerRequest } from "@codex-app-server/ServerRequest";
import type { Turn } from "@codex-app-server/v2/Turn";
import { describe, expect, it, vi } from "vitest";

import { conversationRows, sameConversationRow } from "./conversationRows";
import { createPreviewSeed, previewSessionIds } from "@/preview/fixtures";
import { primaryPreviewServerId } from "@/preview/config";
import { TurnDetails } from "./turnDetails";

describe("会话 FlashList Row 稳定性", () => {
  it("流式更新只重渲染变化正文，稳定用户行可以复用", () => {
    const value = fixtureTurn();
    value.status = "inProgress";
    const before = conversationRows([value], []);
    const updated = { ...value, items: value.items.map((item) => item.type === "agentMessage"
      ? { ...item, text: `${item.text} update` } : item) };
    const after = conversationRows([updated], []);
    expect(sameConversationRow(before[0]!, after[0]!)).toBe(true);
    const final = before.find((row) => row.kind === "block" && row.block.kind === "final")!;
    expect(sameConversationRow(final, after.find((row) => row.key === final.key)!)).toBe(false);
  });
  it("尾部刷新时保留未变化 Turn 与 Request 的 Row 引用", () => {
    const first = turn("turn-1");
    const active = turn("turn-active", "inProgress");
    const request = { id: "request", method: "item/tool/requestUserInput",
      params: {} } as ServerRequest;
    const before = conversationRows([first, active], [request]);
    const after = conversationRows([first, turn("turn-active", "inProgress")], [request]);

    expect(after[0]).toBe(before[0]);
    expect(after[1]).not.toBe(before[1]);
    expect(after.at(-1)).toBe(before.at(-1));
  });

  it("完成态 1000 条过程不进入投影或 Markdown，只保留用户、入口、final", () => {
    const value = fixtureTurn();
    const process = value.items.find((item) => item.type === "commandExecution")!;
    const final = value.items.at(-1)!;
    value.items = [value.items[0]!, ...Array.from({ length: 1000 }, (_, i) =>
      ({ ...process, id: `tool-${i}` })), final];
    const split = vi.fn(() => [{ key: "0", ast: [] }]);
    const rows = conversationRows([value], [], { paginated: true, splitMarkdown: split });
    expect(rows.map((row) => row.kind)).toEqual(["block", "activity", "block"]);
    expect(split).toHaveBeenCalledTimes(1);
    expect(rows.some((row) => row.kind === "block" && row.block.kind === "tools")).toBe(false);
  });

  it("摘要中的 commentary 不是 final，展开后才显示且只请求目标轮一页", async () => {
    const value = fixtureTurn();
    const commentary = value.items.find((item) => item.type === "agentMessage" &&
      item.phase === "commentary")!;
    value.items = [value.items[0]!, commentary];
    value.itemsView = "summary";
    const loader = vi.fn(async () => ({ items: [commentary], nextCursor: "next" }));
    const details = new TurnDetails(loader);
    const before = conversationRows([value], [], { paginated: true, details: details.snapshot() });
    expect(loader).not.toHaveBeenCalled();
    expect(before.find((row) => row.kind === "activity")).toMatchObject({ noFinal: true });
    details.toggle(value, true);
    await details.load(value);
    const after = conversationRows([value], [], { paginated: true, details: details.snapshot() });
    expect(loader).toHaveBeenCalledExactlyOnceWith(value.id, null, "asc");
    expect(after.some((row) => row.kind === "detailPage")).toBe(true);
  });

  it("缺少 phase 的完成回答首屏可见，详情分页和重复展开不丢失或重复正文", async () => {
    const value = fixtureTurn();
    const answer = { type: "agentMessage" as const, id: "unlabelled-answer", phase: null,
      text: "已完成提交和测试。", memoryCitation: null };
    const user = value.items[0]!;
    value.items = [user, answer];
    value.itemsView = "summary";
    const process = { ...answer, id: "process", text: "正在检查", phase: "commentary" as const };
    const loader = vi.fn(async (_id: string, cursor: string | null) => ({
      items: cursor ? [answer] : [user, process], nextCursor: cursor ? null : "next" }));
    const details = new TurnDetails(loader);
    const assertAnswer = () => {
      const rows = conversationRows([value], [], { paginated: true, details: details.snapshot() });
      expect(rows.find((row) => row.kind === "activity")).toMatchObject({ noFinal: false });
      expect(rows.filter((row) => row.kind === "block" && "item" in row.block &&
        row.block.item.id === answer.id)).toHaveLength(1);
      expect(rows.at(-1)).toMatchObject({ kind: "block", block: { kind: "final", item: answer } });
    };
    assertAnswer();
    expect(loader).not.toHaveBeenCalled();
    details.toggle(value, true);
    assertAnswer();
    await details.load(value);
    assertAnswer();
    await details.load(value);
    assertAnswer();
    details.toggle(value, true);
    details.toggle(value, true);
    assertAnswer();
    expect(loader).toHaveBeenCalledTimes(2);
  });

  it("null phase 流式消息完成后收起，回答仍保留在外层", () => {
    const running = { ...turn("running", "inProgress"), items: [{ type: "agentMessage" as const,
      id: "answer", phase: null, text: "完整回答", memoryCitation: null }] };
    const before = conversationRows([running], []);
    expect(before.find((row) => row.kind === "activity")).toMatchObject({ expanded: true });
    const after = conversationRows([{ ...running, status: "completed" }], []);
    expect(after.find((row) => row.kind === "activity")).toMatchObject({ expanded: false, noFinal: false });
    expect(after.at(-1)).toMatchObject({ kind: "block", block: { kind: "final" } });
  });

  it("legacy 展开前不计算过程，展开后工具操作成为独立行", () => {
    const value = fixtureTurn();
    const details = new TurnDetails(vi.fn());
    details.toggle(value, false);
    const before = conversationRows([value], [], { details: details.snapshot() });
    const group = before.find((row) => row.kind === "block" && row.block.kind === "tools")!;
    const expanded = conversationRows([value], [], { details: details.snapshot(),
      expandedTools: new Set([group.key]) });
    expect(expanded.some((row) => row.kind === "operation")).toBe(true);
    details.toggle(value, false);
    const closed = conversationRows([value], [], { details: details.snapshot() });
    expect(closed.some((row) => row.kind === "operation" ||
      row.kind === "block" && row.block.kind === "tools")).toBe(false);
  });
});

function fixtureTurn(): Turn {
  return createPreviewSeed().controls[primaryPreviewServerId]!.threads.find((thread) =>
    thread.id === previewSessionIds.long)!.turns[15]!;
}

function turn(id: string, status: Turn["status"] = "completed"): Turn {
  return { id, status, items: [], itemsView: "full", error: null, startedAt: null,
    completedAt: null, durationMs: null };
}
