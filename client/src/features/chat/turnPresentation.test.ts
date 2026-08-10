import type { ThreadItem } from "@codex-app-server/v2/ThreadItem";
import type { Turn } from "@codex-app-server/v2/Turn";
import { describe, expect, it } from "vitest";

import { createToolGroup, formatDuration, mixedToolGroupTitle, projectTurnPresentation,
  reasoningActivityHeading, toolGroupTitle, toolOperationLines, turnActivitySummary,
  type ToolGroupCategory,
  type ToolItem } from "./turnPresentation";

describe("官方 Turn 移动展示投影", () => {
  it("保持 steer 顺序，并让 commentary 与用户输入切断连续工具组", () => {
    const value = turn("inProgress", [
      user("user-1", "开始"),
      agent("commentary-1", "先检查", "commentary"),
      command("command-1", "completed"),
      command("command-2", "inProgress"),
      user("user-steer", "改用另一种方式"),
      command("command-3", "completed"),
    ]);

    const result = projectTurnPresentation(value);

    expect(result.blocks.map((block) => `${block.kind}:${block.key}`)).toEqual([
      "user:user-1", "commentary:commentary-1", "tools:command-1",
      "user:user-steer", "tools:command-3",
    ]);
    expect(result.blocks[2]).toMatchObject({ running: true, title: "正在运行命令",
      items: [{ id: "command-1" }, { id: "command-2" }] });
    expect(result.hasFinalContent).toBe(false);
    expect(result.canCollapseActivity).toBe(false);
  });

  it("reasoning、Plan 与最终回答都会切断连续工具组", () => {
    const result = projectTurnPresentation(turn("inProgress", [
      command("command-1"),
      { type: "reasoning", id: "reasoning", summary: ["检查"], content: [] },
      command("command-2"),
      { type: "plan", id: "plan", text: "1. 实现" },
      command("command-3"),
      agent("final", "完成", "final_answer"),
      command("command-4"),
    ]));

    expect(result.blocks.map((block) => `${block.kind}:${block.key}`)).toEqual([
      "tools:command-1", "tools:command-2", "plan:plan",
      "tools:command-3", "final:final", "tools:command-4",
    ]);
    expect(result.blocks[0]).toMatchObject({ kind: "tools",
      items: [{ id: "command-1" }] });
  });

  it("把官方 reasoning Markdown 标题投影为干净的活动标题", () => {
    expect(reasoningActivityHeading([
      "前一段摘要",
      "**Reassessing query execution and skill usage**",
    ])).toBe("Reassessing query execution and skill usage");
    expect(reasoningActivityHeading(["### 检查协议顺序"])).toBe("检查协议顺序");
    expect(reasoningActivityHeading(["检查普通摘要"])).toBe("检查普通摘要");
    expect(reasoningActivityHeading(["  "])).toBeNull();

    const completed = projectTurnPresentation(turn("completed", [
      { type: "reasoning", id: "reasoning-markdown",
        summary: ["**Planning evidence-backed rerun**"], content: [] },
      agent("final", "完成", "final_answer"),
    ]));
    const running = projectTurnPresentation(turn("inProgress", [
      command("command-before-reasoning"),
      { type: "reasoning", id: "reasoning-markdown",
        summary: ["**Planning evidence-backed rerun**"], content: [] },
    ]));

    expect(completed.blocks.map((block) => block.kind)).toEqual(["final"]);
    expect(running.blocks.at(-1)).toMatchObject({ kind: "reasoning",
      heading: "Planning evidence-backed rerun" });
  });

  it("长任务只保留当前 reasoning 标题，但保留 reasoning 分隔的全部工具批次", () => {
    const items: ThreadItem[] = [user("user", "执行")];
    for (let index = 0; index < 60; index += 1) {
      items.push({ type: "reasoning", id: `reasoning-${index}`,
        summary: [`**步骤 ${index}**`], content: [] });
      items.push(command(`command-${index}`));
    }
    items.push(agent("commentary", "阶段完成", "commentary"));
    items.push({ type: "reasoning", id: "reasoning-current",
      summary: ["**正在汇总**"], content: [] });

    const result = projectTurnPresentation(turn("inProgress", items));

    const toolBlocks = result.blocks.filter((block) => block.kind === "tools");
    expect(toolBlocks).toHaveLength(60);
    expect(toolBlocks.map((block) => block.items[0]?.id)).toEqual([
      ...Array.from({ length: 60 }, (_, index) => `command-${index}`),
    ]);
    expect(result.blocks.at(-2)).toMatchObject({ kind: "commentary" });
    expect(result.blocks.at(-1)).toMatchObject({ heading: "正在汇总" });
  });

  it("工具完成后 Turn 仍在运行时恢复最近 reasoning，工具运行中则优先显示工具", () => {
    const reasoning = { type: "reasoning", id: "reasoning",
      summary: ["**继续检查结果**"], content: [] } as ThreadItem;
    const completedTool = projectTurnPresentation(turn("inProgress", [
      reasoning, command("completed-tool", "completed"),
    ]));
    const runningTool = projectTurnPresentation(turn("inProgress", [
      reasoning, command("running-tool", "inProgress"),
    ]));

    expect(completedTool.blocks.at(-1)).toMatchObject({ kind: "reasoning",
      heading: "继续检查结果" });
    expect(runningTool.blocks.map((block) => block.kind)).toEqual(["tools"]);
  });

  it("最终回答首段出现即允许折叠，不依赖 Turn 完成", () => {
    const result = projectTurnPresentation(turn("inProgress", [
      user("user", "执行"), command("command", "completed"),
      agent("final", "第一段结果", "final_answer"),
    ]));

    expect(result.hasFinalContent).toBe(true);
    expect(result.canCollapseActivity).toBe(true);
  });

  it("空活动 Turn 显示正在思考，第一条过程或回答到达后让位", () => {
    expect(projectTurnPresentation(turn("inProgress", [user("user", "执行")])).showThinking)
      .toBe(true);
    expect(projectTurnPresentation(turn("inProgress", [user("user", "执行"),
      agent("commentary", "开始检查", "commentary")])).showThinking).toBe(false);
    expect(projectTurnPresentation(turn("inProgress", [user("user", "执行"),
      agent("final", "结果", "final_answer")])).showThinking).toBe(false);
    expect(projectTurnPresentation(turn("completed", [user("user", "执行")])).showThinking)
      .toBe(false);
  });

  it("完成 Turn 的最后一个未知 phase 才回退为最终回答", () => {
    const completed = projectTurnPresentation(turn("completed", [
      agent("unknown-1", "过程", null), agent("unknown-2", "结果", null),
    ]));
    const running = projectTurnPresentation(turn("inProgress", [
      agent("unknown-running", "仍是过程", null),
    ]));

    expect(completed.blocks.map((block) => block.kind)).toEqual(["commentary", "final"]);
    expect(running.blocks.map((block) => block.kind)).toEqual(["commentary"]);
  });

  it("Plan 和显式 final_answer 都不吞掉最后一个未知 phase 最终回答", () => {
    const withPlan = projectTurnPresentation(turn("completed", [
      agent("unknown", "结果", null), { type: "plan", id: "plan", text: "1. 实现" },
    ]));
    const withExplicitFinal = projectTurnPresentation(turn("completed", [
      agent("unknown", "过程", null), agent("final", "结果", "final_answer"),
    ]));

    expect(withPlan.blocks.map((block) => block.kind)).toEqual(["final", "plan"]);
    expect(withExplicitFinal.blocks.map((block) => block.kind)).toEqual(["final", "final"]);
  });

  it("中断 Turn 不自动折叠，Plan 作为最终内容", () => {
    const result = projectTurnPresentation(turn("interrupted", [
      command("command", "completed"), { type: "plan", id: "plan", text: "1. 实现" },
    ]));
    expect(result.hasFinalContent).toBe(true);
    expect(result.canCollapseActivity).toBe(false);
  });

  it("失败且没有最终内容时保持过程展开", () => {
    const result = projectTurnPresentation(turn("failed", [
      agent("commentary", "正在定位错误", "commentary"), command("command", "failed"),
    ]));
    expect(result.hasActivity).toBe(true);
    expect(result.hasFinalContent).toBe(false);
    expect(result.canCollapseActivity).toBe(false);
  });

  it("为每类官方工具提供运行、完成和失败标题", () => {
    const titles: [ToolGroupCategory, string, string, string][] = [
      ["command", "正在运行命令", "运行了命令", "运行命令失败"],
      ["file", "正在修改文件", "修改了文件", "修改文件失败"],
      ["search", "正在搜索网页", "搜索了网页", "搜索网页失败"],
      ["image", "正在处理图片", "处理了图片", "图片操作失败"],
      ["collaboration", "正在协调协作任务", "协调了协作任务", "协作任务失败"],
      ["mcp", "正在调用 MCP 工具", "调用了 MCP 工具", "MCP 工具调用失败"],
      ["dynamic", "正在调用动态工具", "调用了动态工具", "动态工具调用失败"],
      ["wait", "正在等待", "完成了等待", "等待失败"],
      ["context", "正在压缩会话上下文", "压缩了会话上下文", "压缩上下文失败"],
      ["review", "正在进行代码审查", "完成了代码审查", "代码审查操作失败"],
      ["mixed", "正在使用工具", "使用了工具", "工具调用失败"],
    ];
    for (const [category, running, completed, failed] of titles) {
      expect(toolGroupTitle(category, true, false)).toBe(running);
      expect(toolGroupTitle(category, false, false)).toBe(completed);
      expect(toolGroupTitle(category, false, true)).toBe(failed);
    }
  });

  it("识别官方 imageGeneration generating 状态并让不同工具形成混合批次", () => {
    const image = { type: "imageGeneration", id: "image", status: "generating",
      revisedPrompt: null, result: "" } as ToolItem;
    expect(createToolGroup([image])).toMatchObject({ running: true, category: "image",
      title: "正在处理图片" });
    expect(createToolGroup([command("command") as ToolItem, image])).toMatchObject({
      running: true, category: "mixed", title: "正在运行命令、正在处理图片",
    });
  });

  it("混合工具折叠头按官方方式汇总类别和数量", () => {
    const file = { type: "fileChange", id: "file", status: "completed", changes: [
      { path: "a.ts", kind: { type: "update", move_path: null }, diff: "" },
      { path: "b.ts", kind: { type: "create" }, diff: "" },
    ] } as ToolItem;
    const mcp = { type: "mcpToolCall", id: "mcp", server: "filesystem", tool: "read_file",
      status: "completed", arguments: null, appContext: null, pluginId: null,
      readOnlyHint: true, result: null, error: null, durationMs: 2 } as ToolItem;

    expect(mixedToolGroupTitle([file, mcp], false, false))
      .toBe("调用了 1 个 MCP 工具、修改了 2 个文件");
    expect(createToolGroup([file, mcp])).toMatchObject({ category: "mixed",
      title: "调用了 1 个 MCP 工具、修改了 2 个文件" });
  });

  it("用活动 Turn 的尾部位置补足无 status 的网页搜索运行态", () => {
    const search = { type: "webSearch", id: "search", query: "Codex App",
      action: null, results: null } as ToolItem;
    const running = projectTurnPresentation(turn("inProgress", [search]));
    const completed = projectTurnPresentation(turn("completed", [search]));

    expect(running.blocks[0]).toMatchObject({ kind: "tools", running: true,
      inferredRunning: true, title: "正在搜索网页" });
    expect(toolOperationLines(search, true)[0]?.text).toBe("正在搜索 Codex App");
    expect(completed.blocks[0]).toMatchObject({ kind: "tools", running: false,
      inferredRunning: false, title: "搜索了网页" });
  });

  it("展开内容只描述操作，不包含工具输出或错误正文", () => {
    const item = { type: "mcpToolCall", id: "mcp", server: "filesystem", tool: "read_file",
      status: "failed", arguments: null, appContext: null, pluginId: null, readOnlyHint: true,
      result: null, error: null, durationMs: 2 } as ThreadItem;
    expect(toolOperationLines(item as Parameters<typeof toolOperationLines>[0]))
      .toEqual([{ key: "mcp", text: "调用失败 filesystem · read_file",
        running: false, failed: true }]);
  });

  it("混合运行批次按每个 Item 自身状态生成操作行", () => {
    expect(toolOperationLines(command("running", "inProgress") as ToolItem)[0])
      .toMatchObject({ text: "正在运行 echo running", running: true, failed: false });
    expect(toolOperationLines(command("done", "completed") as ToolItem)[0])
      .toMatchObject({ text: "已运行 echo done", running: false, failed: false });
    expect(toolOperationLines(command("failed", "failed") as ToolItem)[0])
      .toMatchObject({ text: "运行失败 echo failed", running: false, failed: true });
  });

  it("为文件、搜索、图片、协作和动态工具生成简化操作行", () => {
    const examples: [ToolItem, string][] = [
      [{ type: "fileChange", id: "file", status: "completed",
        changes: [{ path: "client/App.tsx", kind: { type: "update", move_path: null },
          diff: "" }] } as ToolItem,
      "已修改 client/App.tsx"],
      [{ type: "webSearch", id: "search", query: "Codex App", action: null,
        results: null } as ToolItem, "已搜索 Codex App"],
      [{ type: "imageView", id: "view", path: "/tmp/ui.png" } as ToolItem,
      "已查看 /tmp/ui.png"],
      [{ type: "imageGeneration", id: "image", status: "generating",
        revisedPrompt: null, result: "" } as ToolItem, "正在生成图片"],
      [{ type: "dynamicToolCall", id: "dynamic", namespace: "browser", tool: "open",
        arguments: null, status: "completed", contentItems: null, success: true,
        durationMs: 2 } as ToolItem, "已调用 browser · open"],
      [{ type: "collabAgentToolCall", id: "collab", tool: "spawnAgent", status: "completed",
        senderThreadId: "sender", receiverThreadIds: ["receiver"], prompt: null,
        model: null, reasoningEffort: null, agentsStates: {} } as ToolItem,
      "已启动协作任务"],
      [{ type: "sleep", id: "sleep", durationMs: 2_000 } as ToolItem, "已等待 2秒"],
      [{ type: "contextCompaction", id: "context" } as ToolItem, "已压缩会话上下文"],
      [{ type: "enteredReviewMode", id: "review", review: "变更" } as ToolItem,
      "已开始代码审查"],
    ];
    for (const [item, text] of examples) {
      expect(toolOperationLines(item)[0]?.text).toBe(text);
    }
  });

  it("按官方摘要格式显示秒、分钟和小时", () => {
    expect(formatDuration(37_000)).toBe("37秒");
    expect(formatDuration(184_000)).toBe("3分钟 4秒");
    expect(formatDuration(3_720_000)).toBe("1小时 2分钟");
    const value = turn("inProgress", [], { startedAt: 100 });
    expect(turnActivitySummary(value, 137_000)).toBe("耗时 37秒");
  });
});

function turn(status: Turn["status"], items: ThreadItem[], values: Partial<Turn> = {}): Turn {
  return { id: "turn", status, items, itemsView: "full", error: null, startedAt: 1,
    completedAt: status === "inProgress" ? null : 2, durationMs: null, ...values };
}

function user(id: string, text: string): ThreadItem {
  return { type: "userMessage", id, clientId: id,
    content: [{ type: "text", text, text_elements: [] }] };
}

function agent(id: string, text: string, phase: "commentary" | "final_answer" | null): ThreadItem {
  return { type: "agentMessage", id, text, phase, memoryCitation: null };
}

function command(id: string, status: "inProgress" | "completed" | "failed" = "completed"):
  ThreadItem {
  return { type: "commandExecution", id, pluginId: null, scriptPath: null,
    command: `echo ${id}`, cwd: "/workspace", processId: null, source: "agent", status,
    commandActions: [], aggregatedOutput: null, exitCode: status === "completed" ? 0 : null,
    durationMs: null };
}
