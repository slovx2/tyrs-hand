import type { ThreadItem } from "@codex-app-server/v2/ThreadItem";
import type { Turn } from "@codex-app-server/v2/Turn";
import { describe, expect, it } from "vitest";

import { projectTurnForMobile } from "./mobileProjection";

describe("移动端官方 Item 投影", () => {
  it("在进入 Store 前移除全部工具输出并保留操作顺序", () => {
    const items = [
      command(),
      { type: "fileChange", id: "file", status: "completed",
        changes: [{ path: "client/App.tsx", kind: "update", diff: "large private diff" }] },
      { type: "mcpToolCall", id: "mcp", server: "filesystem", tool: "read_file",
        status: "failed", arguments: { path: "/secret" }, appContext: null, pluginId: null,
        readOnlyHint: true, result: { content: ["secret"], structuredContent: null, _meta: null },
        error: { message: "private error" }, durationMs: 5 },
      { type: "dynamicToolCall", id: "dynamic", namespace: "demo", tool: "lookup",
        arguments: { token: "hidden" }, status: "completed",
        contentItems: [{ type: "inputText", text: "result" }], success: true, durationMs: 6 },
      { type: "collabAgentToolCall", id: "collab", tool: "spawnAgent", status: "completed",
        senderThreadId: "sender", receiverThreadIds: ["receiver"], prompt: "hidden prompt",
        model: null, reasoningEffort: null, agentsStates: { receiver: { status: "completed" } } },
      { type: "webSearch", id: "search", query: "official app", action: null,
        results: [{ title: "hidden result" }] },
      { type: "imageGeneration", id: "image", status: "completed",
        revisedPrompt: "hidden prompt", result: "base64-output", savedPath: "/tmp/image.png" },
      { type: "agentMessage", id: "answer", text: "done", phase: "final_answer",
        memoryCitation: null },
    ] as ThreadItem[];
    const turn: Turn = { id: "turn", status: "completed", items, itemsView: "full",
      error: null, startedAt: 1, completedAt: 2, durationMs: 1000 };

    const projected = projectTurnForMobile(turn);

    expect(projected.items.map((item) => item.id)).toEqual(
      ["command", "file", "mcp", "dynamic", "collab", "search", "image", "answer"]);
    expect(projected.items[0]).toMatchObject({ aggregatedOutput: null, commandActions: [] });
    expect(projected.items[1]).toMatchObject({ changes: [{ path: "client/App.tsx", diff: "" }] });
    expect(projected.items[2]).toMatchObject({ arguments: null, result: null, error: null,
      status: "failed" });
    expect(projected.items[3]).toMatchObject({ arguments: null, contentItems: null, success: true });
    expect(projected.items[4]).toMatchObject({ prompt: null, agentsStates: {} });
    expect(projected.items[5]).toMatchObject({ query: "official app", results: null });
    expect(projected.items[6]).toMatchObject({ revisedPrompt: null, result: "" });
    expect(projected.items[6]).not.toHaveProperty("savedPath");
    expect(projected.items[7]).toBe(items[7]);
    expect((items[0] as Extract<ThreadItem, { type: "commandExecution" }>).aggregatedOutput)
      .toBe("large output");
  });
});

function command(): ThreadItem {
  return { type: "commandExecution", id: "command", pluginId: null, scriptPath: null,
    command: "git status", cwd: "/workspace", processId: null, source: "agent",
    status: "completed", commandActions: [{ type: "unknown", command: "git status" }],
    aggregatedOutput: "large output", exitCode: 0, durationMs: 4 } as ThreadItem;
}
