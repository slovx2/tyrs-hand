import { describe, expect, it } from "vitest";
import type { MobileTurn } from "@/app-server/types";
import { visibleTurnAnswers } from "./turnAnswers";

const unknown = { type: "agentMessage" as const, id: "answer", text: "完成的正文",
  phase: null, memoryCitation: null };
const turn: MobileTurn = { id: "turn", status: "completed", items: [unknown], itemsView: "summary",
  error: null, startedAt: null, completedAt: null, durationMs: null };

describe("收起后可见的回答", () => {
  it("摘要和完整历史都支持 null phase，显式回答优先", () => {
    expect(visibleTurnAnswers(turn)).toEqual([unknown]);
    expect(visibleTurnAnswers({ ...turn, itemsView: "full" })).toEqual([unknown]);
    const explicit = { ...unknown, id: "explicit", phase: "final_answer" as const };
    expect(visibleTurnAnswers({ ...turn, items: [explicit, unknown] })).toEqual([explicit]);
  });

  it.each(["inProgress", "failed", "interrupted"] as const)("%s 不把过程文本提升为回答", (status) => {
    expect(visibleTurnAnswers({ ...turn, status })).toEqual([]);
  });

  it("显式 commentary、空白正文和后续还有非文本 Item 时不回退", () => {
    expect(visibleTurnAnswers({ ...turn, items: [{ ...unknown, phase: "commentary" }] })).toEqual([]);
    expect(visibleTurnAnswers({ ...turn, items: [{ ...unknown, text: " \n " }] })).toEqual([]);
    expect(visibleTurnAnswers({ ...turn, items: [unknown,
      { type: "plan", id: "plan", text: "计划" }] })).toEqual([]);
  });
});
