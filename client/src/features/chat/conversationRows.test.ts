import type { ServerRequest } from "@codex-app-server/ServerRequest";
import type { Turn } from "@codex-app-server/v2/Turn";
import { describe, expect, it } from "vitest";

import { conversationRows } from "./conversationRows";

describe("会话 FlashList Row 稳定性", () => {
  it("尾部刷新时保留未变化 Turn 与 Request 的 Row 引用", () => {
    const first = turn("turn-1");
    const active = turn("turn-active", "inProgress");
    const request = { id: "request", method: "item/tool/requestUserInput",
      params: {} } as ServerRequest;
    const before = conversationRows([first, active], [request]);
    const after = conversationRows([first, turn("turn-active", "inProgress")], [request]);

    expect(after[0]).toBe(before[0]);
    expect(after[1]).not.toBe(before[1]);
    expect(after[2]).toBe(before[2]);
  });
});

function turn(id: string, status: Turn["status"] = "completed"): Turn {
  return { id, status, items: [], itemsView: "full", error: null, startedAt: null,
    completedAt: null, durationMs: null };
}
