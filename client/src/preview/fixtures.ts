import type { Model } from "@codex-app-server/v2/Model";
import type { ServerRequest } from "@codex-app-server/ServerRequest";
import type { Thread } from "@codex-app-server/v2/Thread";
import type { Turn } from "@codex-app-server/v2/Turn";

import type { Connection } from "@/db/connections";
import type { ControlBootstrap } from "@/types/control";
import { primaryPreviewServerId, secondaryPreviewServerId } from "./config";

export type PreviewControlSeed = {
  bootstrap: ControlBootstrap;
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
      agent("agent-running", "我正在读取官方 Thread 与 Turn 状态。", "commentary"),
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
      agent("agent-markdown", "## 验证结果\n\n官方 `Thread -> Turn -> Item` 已成为唯一顺序来源。\n\n- 列表稳定\n- 计划可执行", "final_answer"),
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
    if (number === 16) items.splice(1, 0, command("command-long-16"));
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
      [primaryPreviewServerId]: { bootstrap: bootstrap(primaryPreviewServerId,
        primaryWorkspaceId, primaryProjectId, "Tyrs Hand", "tyrs-hand",
        "/preview/workspaces/tyrs-hand"),
      threads: [running, planned, interactive, failed, markdown, long, archived],
      archivedThreadIds: [archived.id], requests: [{ id: "preview-question",
        method: "item/tool/requestUserInput", params: { threadId: interactive.id,
          turnId: "turn-interactive", itemId: "question-preview", isBlocking: true,
          autoResolutionMs: null, questions: [{ id: "scope", header: "实现范围",
            question: "首版优先覆盖哪一项？", isOther: true, isSecret: false,
            options: [{ label: "完整交互", description: "覆盖计划、问答与停止。" },
              { label: "只读预览", description: "只完成展示。" }] }] } }] },
      [secondaryPreviewServerId]: { bootstrap: bootstrap(secondaryPreviewServerId,
        secondaryWorkspaceId, secondaryProjectId, "远程开发示例", "host-worker",
        "/preview/remote/host-worker"), threads: [secondary], archivedThreadIds: [], requests: [] },
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
  return { kind: "control", profileId: serverId, serverId, baseUrl: `preview://${serverId}`,
    name, deviceId: `preview-${serverId}`, active };
}

function bootstrap(serverId: string, workspaceId: string, projectId: string,
  name: string, relativePath: string, absolutePath: string): ControlBootstrap {
  return { serverId, protocolVersion: 4,
    user: { id: "10000000-0000-4000-8000-000000000099", username: "preview" },
    capabilities: { attachments: true, pushNotifications: false, appServerTunnel: true },
    workspaces: [{ id: workspaceId, name }], projects: [{ id: projectId, workspaceId,
      name, relativePath, absolutePath, kind: "git", availabilityStatus: "available",
      branch: "main", dirty: false }] };
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

function command(id: string): Turn["items"][number] {
  return { type: "commandExecution", id, pluginId: null, scriptPath: null,
    command: "pnpm test -- --runInBand", cwd: "/preview/workspaces/tyrs-hand",
    processId: null, source: "agent", status: "completed", commandActions: [],
    aggregatedOutput: Array.from({ length: 24 }, (_, index) =>
      `test-${String(index + 1).padStart(2, "0")} passed`).join("\n"),
    exitCode: 0, durationMs: 1234 };
}
