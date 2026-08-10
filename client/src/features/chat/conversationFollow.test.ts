import type { Turn } from "@codex-app-server/v2/Turn";
import { describe, expect, it } from "vitest";

import { createFollowState, latestTurnPhase, reduceFollowState,
  shouldFollowLatest } from "./conversationFollow";

describe("会话滚动跟随状态机", () => {
  it("用户上滑超过 24px 立即退出最终回答跟随", () => {
    const state = reduceFollowState(createFollowState("user_follow"), {
      type: "scroll_distance_changed", distanceFromBottomPx: 25,
      latestTurnPhase: "final_answer",
    });
    expect(state.followMode).toBe("static");
    expect(shouldFollowLatest(state)).toBe(false);
  });

  it("处理阶段上滑进入观察态，主动回底恢复处理跟随", () => {
    const watching = reduceFollowState(createFollowState("prework_follow"), {
      type: "scroll_distance_changed", distanceFromBottomPx: 80, latestTurnPhase: "prework",
    });
    expect(watching.followMode).toBe("prework_watch");
    expect(reduceFollowState(watching, { type: "user_reached_bottom",
      latestTurnPhase: "prework" }).followMode).toBe("prework_follow");
  });

  it("处理阶段转最终回答时只延续明确跟随意图", () => {
    const event = { type: "latest_turn_phase_changed" as const,
      previousLatestTurnPhase: "prework" as const, latestTurnPhase: "final_answer" as const };
    expect(reduceFollowState(createFollowState("prework_follow"), event).followMode)
      .toBe("user_follow");
    expect(reduceFollowState(createFollowState("prework_watch"), event).followMode)
      .toBe("static");
  });

  it("显式回底按当前阶段进入对应跟随态", () => {
    expect(reduceFollowState(createFollowState(), { type: "scroll_to_bottom",
      latestTurnPhase: "prework" }).followMode).toBe("prework_follow");
    expect(reduceFollowState(createFollowState(), { type: "scroll_to_bottom",
      latestTurnPhase: "idle" }).followMode).toBe("user_follow");
  });

  it("从 Turn Item 推导处理与最终回答阶段", () => {
    const value = turn();
    value.items.push({ type: "commandExecution", id: "tool", command: "pwd", cwd: "/",
      processId: null, source: "agent", status: "inProgress", commandActions: [],
      aggregatedOutput: null, exitCode: null, durationMs: null, pluginId: null, scriptPath: null });
    expect(latestTurnPhase(value)).toBe("prework");
    value.items.push({ type: "agentMessage", id: "answer", text: "done",
      phase: "final_answer", memoryCitation: null });
    expect(latestTurnPhase(value)).toBe("final_answer");
  });
});

function turn(): Turn {
  return { id: "turn", status: "inProgress", items: [], itemsView: "full", error: null,
    startedAt: 1, completedAt: null, durationMs: null };
}
