import { describe, expect, it } from "vitest";

import type { RunActivity, RunSnapshot } from "@/types/protocol";
import { buildProjectedRunActivity, buildRunActivity, isUnclosedOperationsPart } from "./runActivity";

function snapshot(timeline: RunSnapshot["timeline"]): RunSnapshot {
  return {
    id: "00000000-0000-4000-8000-000000000197",
    status: "completed",
    actualSettings: { model: "gpt-5.6-sol", reasoningEffort: "high", serviceTier: "fast",
      collaborationMode: "default", settingsVersion: 1 },
    startedAt: "2026-08-04T00:00:00Z",
    finishedAt: "2026-08-04T00:01:00Z",
    errorCode: null,
    errorMessage: null,
    timeline,
    pendingInteractives: [],
  };
}

describe("buildRunActivity", () => {
  it("从真实事件形态生成说明和操作，并用完成事件更新操作状态", () => {
    const result = buildRunActivity(snapshot([
      { sequence: 1, type: "item/completed", occurredAt: "2026-08-04T00:00:01Z",
        payload: { item: { id: "commentary-1", type: "agentMessage", phase: "commentary",
          text: "先定位 issue 197 的回归范围。" } } },
      { sequence: 2, type: "item/started", occurredAt: "2026-08-04T00:00:02Z",
        payload: { item: { id: "command-1", type: "commandExecution", command: "rg -n issue-197" } } },
      { sequence: 3, type: "item/completed", occurredAt: "2026-08-04T00:00:03Z",
        payload: { item: { id: "command-1", type: "commandExecution", command: "rg -n issue-197",
          status: "completed" } } },
      { sequence: 4, type: "item/completed", occurredAt: "2026-08-04T00:00:04Z",
        payload: { item: { id: "commentary-2", type: "agentMessage", phase: "commentary",
          text: "已经确认回归点。" } } },
    ]));

    expect(result).toEqual([
      { kind: "commentary", id: "commentary-1", text: "先定位 issue 197 的回归范围。" },
      { kind: "operations", id: "operations-command-1", operations: [
        { id: "command-1", label: "已运行命令 rg -n issue-197", status: "completed" },
      ] },
      { kind: "commentary", id: "commentary-2", text: "已经确认回归点。" },
    ]);
  });

  it("不向界面暴露未知的内部事件名称", () => {
    const result = buildRunActivity(snapshot([
      { sequence: 1, type: "thread/internal_state_changed", occurredAt: "2026-08-04T00:00:01Z",
        payload: {} },
    ]));
    expect(result).toEqual([{ kind: "operations", id: "operations-event-1",
      operations: [{ id: "event-1", label: "处理任务", status: "completed" }] }]);
  });
});

describe("buildProjectedRunActivity", () => {
  it("将服务端分页活动保持为自包含说明和操作组", () => {
    const activities: RunActivity[] = [
      { id: "00000000-0000-4000-8000-000000000301", itemId: "commentary", kind: "commentary",
        firstEventSequence: 2, lastEventSequence: 4, status: "completed",
        payload: { text: "已合并完整 commentary" }, occurredAt: "2026-08-04T00:00:01Z" },
      { id: "00000000-0000-4000-8000-000000000302", itemId: "tool", kind: "operation",
        firstEventSequence: 5, lastEventSequence: 6, status: "completed",
        payload: { eventType: "item/completed", item: { type: "webSearch", query: "分页" } },
        occurredAt: "2026-08-04T00:00:02Z" },
    ];
    expect(buildProjectedRunActivity(activities)).toEqual([
      { kind: "commentary", id: activities[0]!.id, text: "已合并完整 commentary" },
      { kind: "operations", id: `operations-${activities[1]!.id}`, operations: [
        { id: "tool", label: "已搜索 分页", status: "completed" },
      ] },
    ]);
  });

  it("仅在工具组后没有 commentary 或 final answer 时保持未闭合", () => {
    const operation: RunActivity = {
      id: "00000000-0000-4000-8000-000000000303", itemId: "tool", kind: "operation",
      firstEventSequence: 5, lastEventSequence: 6, status: "completed",
      payload: { eventType: "item/completed", item: { type: "webSearch", query: "流式" } },
      occurredAt: "2026-08-04T00:00:02Z",
    };
    const trailingOperations = buildProjectedRunActivity([operation]);
    expect(isUnclosedOperationsPart(trailingOperations, 0, true, false)).toBe(true);
    expect(isUnclosedOperationsPart(trailingOperations, 0, true, true)).toBe(false);
    expect(isUnclosedOperationsPart(trailingOperations, 0, false, false)).toBe(false);

    const followedByCommentary = buildProjectedRunActivity([operation, {
      id: "00000000-0000-4000-8000-000000000304", itemId: "commentary", kind: "commentary",
      firstEventSequence: 7, lastEventSequence: 8, status: "completed",
      payload: { text: "工具调用完成，继续处理。" }, occurredAt: "2026-08-04T00:00:03Z",
    }]);
    expect(isUnclosedOperationsPart(followedByCommentary, 0, true, false)).toBe(false);
  });
});
