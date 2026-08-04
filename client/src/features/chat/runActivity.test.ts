import { describe, expect, it } from "vitest";

import type { RunSnapshot } from "@/types/protocol";
import { buildRunActivity } from "./runActivity";

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
