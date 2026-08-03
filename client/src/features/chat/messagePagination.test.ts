import { describe, expect, it } from "vitest";

import type { Message } from "@/types/protocol";
import { mergeMessages } from "./messagePagination";

function message(id: string, seq: number, text = id): Message {
  return { id, sessionId: "00000000-0000-4000-8000-000000000001", seq,
    localId: id, participantId: null, role: "agent", content: { type: "text", text },
    attachments: [], createdAt: "2026-08-03T00:00:00Z", updatedAt: "2026-08-03T00:00:00Z" };
}

describe("mergeMessages", () => {
  it("合并前后分页并保持序号顺序", () => {
    expect(mergeMessages([message("2", 2)], [message("1", 1), message("3", 3)]).
      map((item) => item.seq)).toEqual([1, 2, 3]);
  });

  it("重复事件只保留一条并应用最新快照", () => {
    const result = mergeMessages([message("same", 1, "old")], [message("same", 1, "new")]);
    expect(result).toHaveLength(1);
    expect(result[0]!.content).toEqual({ type: "text", text: "new" });
  });
});
