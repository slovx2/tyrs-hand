import type { Thread } from "@codex-app-server/v2/Thread";
import type { Turn } from "@codex-app-server/v2/Turn";
import { describe, expect, it, vi } from "vitest";

import type { ThreadRecord, UserInputResponseItem } from "@/app-server/types";
import { cacheableThreadRecord } from "./cache";

vi.mock("./database", () => ({
  getDatabase: vi.fn(),
  withDatabaseTransaction: vi.fn(),
}));

describe("Thread 最近页缓存", () => {
  it("SQLite 只保存最近 5 个 Turn，并把旧页游标重置到最新页边界", () => {
    const record = loadedRecord(Array.from({ length: 12 }, (_, index) => turn(index + 1)));

    const cached = cacheableThreadRecord(record);

    expect(cached.thread.turns.map((item) => item.id)).toEqual([
      "turn-8", "turn-9", "turn-10", "turn-11", "turn-12",
    ]);
    expect(cached.history).toEqual({ kind: "loaded", olderCursor: "tail-cursor",
      tailOlderCursor: "tail-cursor", hasLoadedOldest: false });
    expect(record.thread.turns).toHaveLength(12);
  });

  it("已到最早历史的最近页保持终止状态", () => {
    const record = loadedRecord([turn(1), turn(2)]);
    record.history = { kind: "loaded", olderCursor: null, tailOlderCursor: null,
      hasLoadedOldest: true };
    expect(cacheableThreadRecord(record).history).toMatchObject({
      olderCursor: null, hasLoadedOldest: true,
    });
  });

  it("缓存终态 Turn 时收敛缺失快照遗留的运行中工具", () => {
    const record = loadedRecord([turn(1)]);
    record.thread.turns[0]!.items = [command("tool-1")];

    const cached = cacheableThreadRecord(record);

    expect(cached.thread.turns[0]?.items[0]).toMatchObject({ status: "completed" });
  });

  it("最近页缓存保留本地回答回显 Item", () => {
    const record = loadedRecord([turn(1)]);
    const response: UserInputResponseItem = {
      type: "userInputResponse", id: "user-input-response-request-1", requestId: "request-1",
      turnId: "turn-1", questions: [], answers: {}, completed: true,
    };
    record.thread.turns[0]!.items.push(response);

    expect(cacheableThreadRecord(record).thread.turns[0]?.items).toContainEqual(response);
  });
});

function loadedRecord(turns: Turn[]): ThreadRecord {
  return { thread: thread(turns), archived: false, workspaceId: null, projectId: "project-1",
    history: { kind: "loaded", olderCursor: "runtime-cursor",
      tailOlderCursor: "tail-cursor", hasLoadedOldest: false } };
}

function thread(turns: Turn[]): Thread {
  return { id: "thread-1", sessionId: "session-1", forkedFromId: null,
    parentThreadId: null, preview: "thread", ephemeral: false, section: null,
    sectionEnteredAt: null, modelProvider: "openai", createdAt: 1, updatedAt: 2,
    recencyAt: 2, status: { type: "idle" }, path: null, cwd: "/workspace",
    cliVersion: "0.147.0", source: "appServer", threadSource: null,
    agentNickname: null, agentRole: null, gitInfo: null, name: null, turns, extra: null,
    historyMode: "legacy", canAcceptDirectInput: true };
}

function turn(index: number): Turn {
  return { id: `turn-${index}`, status: "completed", items: [], itemsView: "full",
    error: null, startedAt: index, completedAt: index, durationMs: null };
}

function command(id: string): Turn["items"][number] {
  return { type: "commandExecution", id, command: "pwd", cwd: "/workspace",
    processId: null, source: "agent", status: "inProgress", commandActions: [],
    aggregatedOutput: null, exitCode: null, durationMs: null, pluginId: null, scriptPath: null };
}
