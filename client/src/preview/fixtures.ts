import type { Connection } from "@/db/connections";
import type { OutboxItem } from "@/sync/outbox";
import type { Bootstrap, Message, RunSnapshot, Session, SessionSettings } from "@/types/protocol";
import { primaryPreviewServerId, secondaryPreviewServerId } from "./config";

export type PreviewSessionDetail = {
  settings: SessionSettings;
  currentRun: RunSnapshot | null;
  messages: Message[];
};

export type PreviewControlSeed = {
  bootstrap: Bootstrap;
  sessions: Session[];
  details: Record<string, PreviewSessionDetail>;
  outbox: OutboxItem[];
};

export type PreviewSeed = {
  connections: Connection[];
  controls: Record<string, PreviewControlSeed>;
};

const profileId = "20000000-0000-4000-8000-000000000001";
const environmentId = "20000000-0000-4000-8000-000000000002";
const projectId = "20000000-0000-4000-8000-000000000003";
const unavailableProjectId = "20000000-0000-4000-8000-000000000004";
const secondaryProjectId = "20000000-0000-4000-8000-000000000005";
const baseTime = "2026-08-03T10:00:00.000+08:00";

const settings: SessionSettings = {
  agentProfileId: profileId,
  model: "gpt-5.6-sol",
  reasoningEffort: "high",
  serviceTier: "fast",
  collaborationMode: "default",
  settingsVersion: 3,
};

function session(index: number, title: string, overrides: Partial<Session> = {}): Session {
  return {
    id: `30000000-0000-4000-8000-${String(index).padStart(12, "0")}`,
    developmentEnvironmentId: environmentId,
    developmentProjectId: projectId,
    agentProfileId: profileId,
    title,
    lifecycleState: "active",
    historyCompleteness: "complete",
    model: "gpt-5.6-sol",
    reasoningEffort: "high",
    serviceTier: "fast",
    collaborationMode: "default",
    settingsVersion: 3,
    lastMessageSeq: 2,
    lastActivityAt: baseTime,
    createdAt: baseTime,
    updatedAt: baseTime,
    ...overrides,
  };
}

function message(sessionId: string, seq: number, role: Message["role"], text: string,
  attachments: Message["attachments"] = []): Message {
  return {
    id: `40000000-0000-4000-8000-${sessionId.slice(-6)}${String(seq).padStart(6, "0")}`,
    sessionId,
    seq,
    localId: `preview-${sessionId.slice(-4)}-${seq}`,
    participantId: role === "event" ? null : profileId,
    role,
    content: { type: "text", text },
    attachments,
    createdAt: baseTime,
    updatedAt: baseTime,
  };
}

function run(index: number, status: RunSnapshot["status"], mode: "default" | "plan" = "default"):
  RunSnapshot {
  return {
    id: `50000000-0000-4000-8000-${String(index).padStart(12, "0")}`,
    status,
    actualSettings: { model: "gpt-5.6-sol", reasoningEffort: "high", serviceTier: "fast",
      collaborationMode: mode, settingsVersion: 3 },
    startedAt: baseTime,
    finishedAt: ["completed", "failed", "canceled"].includes(status) ? baseTime : null,
    errorCode: null,
    errorMessage: null,
    timeline: [
      { sequence: 1, type: "Turn 已创建", payload: {}, occurredAt: baseTime },
      { sequence: 2, type: "正在分析工作区", payload: {}, occurredAt: baseTime },
      { sequence: 3, type: "读取并修改代码", payload: {}, occurredAt: baseTime },
    ],
    pendingInteractives: [],
  };
}

const runningSession = session(1, "运行中：完善移动端会话体验");
const planSession = session(2, "Plan 已完成：重构同步状态机", { collaborationMode: "plan" });
const interactiveSession = session(3, "等待回答：确认实现范围");
const secretSession = session(4, "Secret：等待 Desktop 输入");
const failedSession = session(5, "执行失败：依赖服务不可用", { serviceTier: "standard" });
const attachmentSession = session(6, "附件与 Markdown 完整展示");
const archivedSession = session(7, "已归档：旧版通知链路", { lifecycleState: "archived" });

const runningRun = run(1, "running");
const planRun = run(2, "completed", "plan");
const interactiveRun = run(3, "waiting_for_user");
interactiveRun.pendingInteractives = [{
  id: "70000000-0000-4000-8000-000000000001",
  status: "pending",
  secret: false,
  deadlineAt: null,
  questions: [{ id: "scope", header: "实现范围", question: "首版优先覆盖哪一项？", options: [
    { label: "完整交互", description: "覆盖会话、Plan、问答、停止与附件。" },
    { label: "只读预览", description: "先完成展示，暂不模拟写操作。" },
  ] }, { id: "note", header: "补充说明", question: "还有什么验收要求？" }],
}];
const secretRun = run(4, "waiting_for_user");
secretRun.pendingInteractives = [{
  id: "70000000-0000-4000-8000-000000000002",
  status: "pending",
  secret: true,
  deadlineAt: null,
  questions: [{ id: "token", header: "访问令牌", question: "请输入部署凭证", isSecret: true }],
}];
const failedRun = run(5, "failed");
failedRun.errorCode = "UPSTREAM_UNAVAILABLE";
failedRun.errorMessage = "无法连接模型服务，请检查网络后重试。";

const imageAttachment = {
  id: "60000000-0000-4000-8000-000000000001", sessionId: attachmentSession.id,
  kind: "image" as const, filename: "mobile-layout.png", mediaType: "image/png", sizeBytes: 248320,
  sha256: "preview-image", status: "attached" as const, createdAt: baseTime,
};
const fileAttachment = {
  id: "60000000-0000-4000-8000-000000000002", sessionId: attachmentSession.id,
  kind: "file" as const, filename: "acceptance-report.md", mediaType: "text/markdown", sizeBytes: 8192,
  sha256: "preview-file", status: "attached" as const, createdAt: baseTime,
};

function conversation(item: Session, answer: string, currentRun: RunSnapshot | null = null):
  PreviewSessionDetail {
  return {
    settings: { ...settings, collaborationMode: item.collaborationMode,
      serviceTier: item.serviceTier, settingsVersion: item.settingsVersion },
    currentRun,
    messages: [
      message(item.id, 1, "user", `请处理：${item.title}`),
      message(item.id, 2, "agent", answer),
    ],
  };
}

const details: Record<string, PreviewSessionDetail> = {
  [runningSession.id]: conversation(runningSession,
    "正在检查 **导航、缓存和输入框**。\n\n```tsx\n<ChatComposer mode=\"preview\" />\n```", runningRun),
  [planSession.id]: conversation(planSession,
    "## 执行计划\n\n1. 统一同步状态\n2. 补齐断线恢复\n3. 验证幂等发送", planRun),
  [interactiveSession.id]: conversation(interactiveSession, "需要确认两个问题后继续。", interactiveRun),
  [secretSession.id]: conversation(secretSession, "下一步需要敏感信息，请在 Desktop 中完成。", secretRun),
  [failedSession.id]: conversation(failedSession, "任务未能完成，错误详情见运行进度。", failedRun),
  [attachmentSession.id]: {
    ...conversation(attachmentSession,
      "已收到截图和报告。\n\n- 图片用于检查遮挡与越界\n- 报告用于核对验收结论"),
    messages: [
      message(attachmentSession.id, 1, "user", "请根据附件验收界面。", [imageAttachment, fileAttachment]),
      message(attachmentSession.id, 2, "agent",
        "## 验收摘要\n\n配色与 WakeQora 基准一致，代码块如下：\n\n```ts\nconst status = 'passed'\n```"),
    ],
  },
  [archivedSession.id]: conversation(archivedSession, "这条会话已经归档，可随时恢复。"),
};

const modelCatalog: Bootstrap["modelCatalog"] = [{
  id: "gpt-5.6-sol",
  displayName: "GPT-5.6 Sol",
  inputModalities: ["text", "image", "file"],
  supportedReasoningEfforts: ["low", "medium", "high", "xhigh", "max", "ultra"].map((id) => ({
    id: id as "low" | "medium" | "high" | "xhigh" | "max" | "ultra",
    description: `${id} 推理强度`,
  })),
  defaultReasoningEffort: "high",
  serviceTiers: [
    { id: "standard", name: "标准", description: "稳定的标准处理速度" },
    { id: "fast", name: "快速", description: "更低延迟的优先处理" },
  ],
  defaultServiceTier: "standard",
  default: true,
}, {
  id: "gpt-5.6-luna",
  displayName: "GPT-5.6 Luna",
  inputModalities: ["text", "image"],
  supportedReasoningEfforts: [{ id: "medium", description: "平衡速度与质量" },
    { id: "high", description: "处理更复杂的任务" }, { id: "max", description: "最大推理强度" }],
  defaultReasoningEffort: "medium",
  serviceTiers: [{ id: "fast", name: "快速", description: "适合大量简单任务" }],
  defaultServiceTier: "fast",
  default: false,
}];

const primaryBootstrap: Bootstrap = {
  serverId: primaryPreviewServerId,
  protocolVersion: 2,
  currentCursor: 128,
  user: { id: "80000000-0000-4000-8000-000000000001", username: "UI 验收员" },
  capabilities: { attachments: true, interactive: true, plan: true, push: true, multiControl: true },
  projects: [{
    id: projectId,
    environmentId,
    name: "Tyrs Hand 移动客户端",
    relativePath: "workspaces/tyrs-hand/client",
    kind: "git",
    availabilityStatus: "available",
    branch: "feature/mobile-preview",
    dirty: true,
  }, {
    id: unavailableProjectId,
    environmentId,
    name: "名称很长的离线项目，用于验证窄屏换行和边界处理",
    relativePath: "workspaces/archive/a-very-long-project-path-for-layout-acceptance",
    kind: "git",
    availabilityStatus: "offline",
    branch: null,
    dirty: false,
  }],
  agentProfiles: [{ id: profileId, name: "Codex 默认" },
    { id: "20000000-0000-4000-8000-000000000006", name: "Luna Agent" }],
  modelCatalog,
  lastStartedSettings: settings,
};

const secondaryBootstrap: Bootstrap = {
  ...primaryBootstrap,
  serverId: secondaryPreviewServerId,
  currentCursor: 12,
  user: { id: "80000000-0000-4000-8000-000000000002", username: "远程开发者" },
  projects: [{
    id: secondaryProjectId,
    environmentId: "20000000-0000-4000-8000-000000000007",
    name: "远程 Worker 示例",
    relativePath: "workspaces/remote-worker",
    kind: "git",
    availabilityStatus: "available",
    branch: "main",
    dirty: false,
  }],
};

const secondarySession = session(8, "另一 Control 的隔离会话", {
  developmentProjectId: secondaryProjectId,
  developmentEnvironmentId: secondaryBootstrap.projects[0]!.environmentId,
});

export function createPreviewSeed(): PreviewSeed {
  const seed: PreviewSeed = {
    connections: [{
      serverId: primaryPreviewServerId,
      baseUrl: "preview://local-control",
      name: "本机 Control · UI 全状态",
      deviceId: "preview-device-primary",
      active: true,
    }, {
      serverId: secondaryPreviewServerId,
      baseUrl: "preview://remote-control-with-a-long-hostname",
      name: "远程 Control · 数据隔离",
      deviceId: "preview-device-secondary",
      active: false,
    }],
    controls: {
      [primaryPreviewServerId]: {
        bootstrap: primaryBootstrap,
        sessions: [runningSession, planSession, interactiveSession, secretSession,
          failedSession, attachmentSession, archivedSession],
        details,
        outbox: [{
          serverId: primaryPreviewServerId,
          localId: "preview-create-failed",
          kind: "create_session",
          sessionId: null,
          projectId,
          status: "failed",
          payload: { text: "这条任务用于展示离线发送失败状态", attachments: [], settings },
          error: "网络不可用，任务已保存在本地",
        }, {
          serverId: primaryPreviewServerId,
          localId: "preview-message-failed",
          kind: "send_message",
          sessionId: runningSession.id,
          projectId: null,
          status: "failed",
          payload: { text: "展示会话内重试和丢弃操作", attachments: [] },
          error: "上传附件时连接中断",
        }],
      },
      [secondaryPreviewServerId]: {
        bootstrap: secondaryBootstrap,
        sessions: [secondarySession],
        details: {
          [secondarySession.id]: conversation(secondarySession,
            "这个会话只存在于远程 Control，用于验证缓存和导航隔离。"),
        },
        outbox: [],
      },
    },
  };
  return structuredClone(seed);
}

export const previewSessionIds = {
  running: runningSession.id,
  plan: planSession.id,
  interactive: interactiveSession.id,
  secret: secretSession.id,
  failed: failedSession.id,
  attachments: attachmentSession.id,
  archived: archivedSession.id,
};
