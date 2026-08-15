import type { Model } from "@codex-app-server/v2/Model";
import type { ServerRequest } from "@codex-app-server/ServerRequest";
import type { Thread } from "@codex-app-server/v2/Thread";
import type { Turn } from "@codex-app-server/v2/Turn";

import type { Connection } from "@/db/connections";
import type { MobileProject } from "@/app-server/types";
import { primaryPreviewServerId, secondaryPreviewServerId } from "./config";

export type PreviewControlSeed = {
  projects: MobileProject[];
  threads: Thread[];
  archivedThreadIds: string[];
  requests: ServerRequest[];
};

export type PreviewSeed = {
  connections: Connection[];
  controls: Record<string, PreviewControlSeed>;
  models: Model[];
};

const primaryWorkspaceId = "20000000-0000-4000-8000-000000000002";
const primaryProjectId = "20000000-0000-4000-8000-000000000003";
const secondaryWorkspaceId = "20000000-0000-4000-8000-000000000007";
const secondaryProjectId = "20000000-0000-4000-8000-000000000005";

export const previewSessionIds = {
  running: "30000000-0000-4000-8000-000000000001",
  plan: "30000000-0000-4000-8000-000000000002",
  interactive: "30000000-0000-4000-8000-000000000003",
  failed: "30000000-0000-4000-8000-000000000005",
  markdown: "30000000-0000-4000-8000-000000000009",
  long: "30000000-0000-4000-8000-000000000010",
  archived: "30000000-0000-4000-8000-000000000007",
};

export function createPreviewSeed(): PreviewSeed {
  const now = Math.floor(Date.now() / 1000);
  const running = thread(previewSessionIds.running, "/preview/workspaces/tyrs-hand",
    "运行中：完善移动端会话体验", [turn("turn-running", "inProgress", [
      user("user-running", "preview-running", "请检查移动端协议时序"),
      { type: "reasoning", id: "reasoning-running",
        summary: ["**检查官方事件顺序**"], content: [] },
      agent("agent-running", "我正在读取官方 Thread 与 Turn 状态。", "commentary"),
      command("command-running-1", "inProgress", "PREVIEW_TOOL_OUTPUT_MUST_NOT_RENDER"),
      command("command-running-2", "inProgress", "STREAMING_OUTPUT_MUST_NOT_RENDER"),
    ])], now);
  const planned = thread(previewSessionIds.plan, "/preview/workspaces/tyrs-hand",
    "计划已完成：优化消息恢复", [turn("turn-plan", "completed", [
      user("user-plan", "preview-plan", "先给出重构计划"),
      { type: "plan", id: "plan-preview", text: "1. 删除旧同步状态机\n2. 使用官方历史恢复\n3. 验证乱序事件" },
    ])], now - 10);
  const interactive = thread(previewSessionIds.interactive, "/preview/workspaces/tyrs-hand",
    "等待回答：确认实现范围", [turn("turn-interactive", "inProgress", [
      user("user-interactive", "preview-interactive", "开始实现并在需要时提问"),
    ])], now - 20);
  const failed = thread(previewSessionIds.failed, "/preview/workspaces/tyrs-hand",
    "执行失败：依赖服务不可用", [{ id: "turn-failed", status: "failed",
      items: [user("user-failed", "preview-failed", "运行验证")], itemsView: "full",
      error: { message: "无法连接模型服务，请检查网络后重试。", codexErrorInfo: null,
        additionalDetails: null }, startedAt: now - 50, completedAt: now - 45, durationMs: 5000 }], now - 30);
  const markdown = thread(previewSessionIds.markdown, "/preview/workspaces/tyrs-hand",
    "Markdown 与工具状态", [turn("turn-markdown", "completed", [
      user("user-markdown", "preview-markdown", "展示完整的官方 Item"),
      agent("agent-markdown", [
        "## 正文渲染预览",
        "",
        "官方 `Thread -> Turn -> Item` 已成为唯一顺序来源。",
        "",
        "文件引用：查看【src/features/chat/OfficialTurn.tsx†L115-L133】和【F:README.md†L5】。",
        "",
        "- [x] 文件引用转换为正文 Chip",
        "- [ ] 右侧来源小框暂不接入",
        "",
        "<strong>基础 HTML 粗体</strong>、<u>下划线</u>、<kbd>Ctrl</kbd>。",
        "",
        ':::task-stub{title="继续对齐官方渲染"}',
        "检查移动端正文特殊元素",
        ":::",
        "",
        ':::writing{variant="email" id="12345"}',
        "主题：渲染能力对齐",
        "正文中的写作块会显示为独立卡片。",
        ":::",
        "",
        '::artifact-template{artifact_kind="document" display_name="移动端渲染规范"}',
        "",
        '::git-commit{cwd="/preview/workspaces/tyrs-hand"}',
        '::code-comment{path="client/src/features/chat/MarkdownContent.tsx"}',
      ].join("\n"), "final_answer"),
    ])], now - 40);
  const longTurns = Array.from({ length: 32 }, (_, index) => {
    const number = index + 1;
    const items: Turn["items"] = [
      user(`user-long-${number}`, `preview-long-${number}`, `长会话第 ${number} 轮`),
      agent(`agent-long-${number}`, number === 32
        ? "## 最新结果\n\nLONG_CONVERSATION_LATEST\n\n这是分页首屏应该直接展示的最后一轮。"
        : `## 第 ${number} 轮结果\n\n${"用于验证 Markdown 高度变化与稳定锚点。\n\n".repeat(3)}`,
      "final_answer"),
    ];
    if (number % 4 === 0) {
      const process: Turn["items"] = [agent(`commentary-long-${number}`,
        `正在检查第 ${number} 轮的上下文、文件状态与验证结果。`, "commentary")];
      if (number % 8 === 0) process.push(command(`command-long-${number}`, "completed",
        `LONG_TOOL_OUTPUT_MUST_NOT_RENDER:${number}`));
      if (number % 16 === 0) process.push({ type: "reasoning", id: `reasoning-long-${number}`,
        summary: [`已核对第 ${number} 轮的关键约束。`], content: [] });
      if (number === 16) {
        for (let step = 1; step <= 60; step += 1) {
          process.push({ type: "reasoning", id: `reasoning-long-${number}-${step}`,
            summary: [`**长任务中间步骤 ${step}**`], content: [] });
          if (step <= 45) process.push(command(`command-long-${number}-${step}`, "completed",
            `LONG_TOOL_OUTPUT_MUST_NOT_RENDER:${number}:${step}`));
          if (step === 1) process.push(fileChange(`file-long-${number}`, [
            "client/src/features/chat/OfficialTurn.tsx",
            "client/src/features/chat/turnPresentation.ts",
          ]));
          if (step === 2) process.push(mcpToolCall(`mcp-long-${number}`));
          if (step % 3 === 0) process.push(agent(`commentary-long-${number}-${step}`,
            `已完成第 ${step} 个中间阶段，继续核对协议、缓存和滚动锚点。`, "commentary"));
        }
      }
      items.splice(1, 0, ...process);
    }
    return turn(`turn-long-${String(number).padStart(2, "0")}`, "completed", items);
  });
  const long = thread(previewSessionIds.long, "/preview/workspaces/tyrs-hand",
    "长会话：32 个 Turn 分页与锚点", longTurns, now - 50);
  const archived = thread(previewSessionIds.archived, "/preview/workspaces/tyrs-hand",
    "已归档：旧版通知链路", [turn("turn-archived", "completed", [
      user("user-archived", "preview-archived", "归档这个历史会话"),
      agent("agent-archived", "已完成。", "final_answer"),
    ])], now - 60);
  const secondary = thread("30000000-0000-4000-8000-000000000008",
    "/preview/remote/host-worker", "另一连接中的独立会话", [turn("turn-secondary", "completed", [
      user("user-secondary", "preview-secondary", "验证 profile 隔离"),
      agent("agent-secondary", "这个会话只存在于第二个 Control profile。", "final_answer"),
    ])], now - 70);
  return {
    connections: [controlConnection(primaryPreviewServerId, "本机服务 · 官方协议", true),
      controlConnection(secondaryPreviewServerId, "远程服务 · 数据隔离", false)],
    controls: {
      [primaryPreviewServerId]: { projects: [project(primaryWorkspaceId, primaryProjectId,
        "Tyrs Hand", "/preview/workspaces/tyrs-hand")],
      threads: [running, planned, interactive, failed, markdown, long, archived],
      archivedThreadIds: [archived.id], requests: [{ id: "preview-question",
        method: "item/tool/requestUserInput", params: { threadId: interactive.id,
          turnId: "turn-interactive", itemId: "question-preview", isBlocking: true,
          autoResolutionMs: null, questions: [{ id: "scope", header: "实现范围",
            question: "首版优先覆盖哪一项？", isOther: true, isSecret: false,
            options: [{ label: "完整交互", description: "覆盖计划、问答与停止。" },
              { label: "只读预览", description: "只完成展示。" }] }] } }] },
      [secondaryPreviewServerId]: { projects: [project(secondaryWorkspaceId, secondaryProjectId,
        "远程开发示例", "/preview/remote/host-worker")], threads: [secondary],
        archivedThreadIds: [], requests: [] },
    },
    models: [{ id: "gpt-5.6-sol", model: "gpt-5.6-sol", displayName: "GPT-5.6 Sol",
      description: "预览模型", modelSpecialty: null, hidden: false, upgrade: null,
      upgradeInfo: null, availabilityNux: null, supportedReasoningEfforts: [
        { reasoningEffort: "high", description: "深入推理" },
        { reasoningEffort: "medium", description: "平衡" }], defaultReasoningEffort: "high",
      inputModalities: ["text", "image"], supportsPersonality: true,
      additionalSpeedTiers: [], serviceTiers: [{ id: "priority", name: "快速",
        description: "优先处理" }], defaultServiceTier: null, isDefault: true }],
  };
}

function controlConnection(serverId: string, name: string, active: boolean): Connection {
  return { kind: "ssh", profileId: serverId, host: "preview.local", port: 2222,
    user: "preview", keyRef: `preview-${serverId}`, hostFingerprint: "preview", name, active,
    machineFingerprint: `preview:${serverId}`, controls: [] };
}

function project(workspaceId: string, projectId: string, name: string,
  cwd: string): MobileProject {
  return { id: projectId, workspaceId, name, relativePath: cwd, cwd, kind: "ssh",
    availabilityStatus: "available", branch: "main", dirty: false };
}

function thread(id: string, cwd: string, name: string, turns: Turn[], updatedAt: number): Thread {
  return { id, extra: null, sessionId: id, forkedFromId: null, parentThreadId: null,
    preview: name, ephemeral: false, section: null, sectionEnteredAt: null, historyMode: "legacy",
    modelProvider: "openai", createdAt: updatedAt - 60, updatedAt, recencyAt: updatedAt,
    status: turns.some((item) => item.status === "inProgress")
      ? { type: "active", activeFlags: [] } : { type: "idle" }, path: null, cwd,
    cliVersion: "0.147.0", source: "appServer", canAcceptDirectInput: true,
    threadSource: null, agentNickname: null, agentRole: null, gitInfo: null, name, turns };
}

function turn(id: string, status: Turn["status"], items: Turn["items"]): Turn {
  const now = Math.floor(Date.now() / 1000);
  return { id, status, items, itemsView: "full", error: null, startedAt: now - 5,
    completedAt: status === "inProgress" ? null : now, durationMs: null };
}

function user(id: string, clientId: string, text: string): Turn["items"][number] {
  return { type: "userMessage", id, clientId,
    content: [{ type: "text", text, text_elements: [] }] };
}

function agent(id: string, text: string, phase: "commentary" | "final_answer"):
  Turn["items"][number] {
  return { type: "agentMessage", id, text, phase, memoryCitation: null };
}

function command(id: string, status: "inProgress" | "completed", output: string):
  Turn["items"][number] {
  return { type: "commandExecution", id, pluginId: null, scriptPath: null,
    command: "pnpm test -- --runInBand", cwd: "/preview/workspaces/tyrs-hand",
    processId: null, source: "agent", status, commandActions: [],
    aggregatedOutput: output, exitCode: status === "completed" ? 0 : null,
    durationMs: status === "completed" ? 1234 : null };
}

function fileChange(id: string, paths: string[]): Turn["items"][number] {
  return { type: "fileChange", id, status: "completed", changes: paths.map((path) => ({
    path, kind: { type: "update", move_path: null }, diff: "MOBILE_DIFF_MUST_NOT_RENDER",
  })) };
}

function mcpToolCall(id: string): Turn["items"][number] {
  return { type: "mcpToolCall", id, server: "filesystem", tool: "read_file",
    status: "completed", arguments: null, appContext: null, pluginId: null, readOnlyHint: true,
    result: null, error: null, durationMs: 250 };
}
