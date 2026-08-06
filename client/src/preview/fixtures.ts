import type { Connection } from "@/db/connections";
import type { OutboxItem } from "@/sync/outbox";
import type { Bootstrap, ConversationTurn, Message, RunActivity, RunSnapshot, Session,
  SessionSettings } from "@/types/protocol";
import { primaryPreviewServerId, secondaryPreviewServerId } from "./config";

export type PreviewSessionDetail = {
  settings: SessionSettings;
  currentRun: RunSnapshot | null;
  messages: Message[];
  turns?: ConversationTurn[];
  activities?: Record<string, RunActivity[]>;
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
const workspaceId = "20000000-0000-4000-8000-000000000002";
const projectId = "20000000-0000-4000-8000-000000000003";
const unavailableProjectId = "20000000-0000-4000-8000-000000000004";
const secondaryProjectId = "20000000-0000-4000-8000-000000000005";
const previewNow = Date.now();
const baseTime = new Date(previewNow - 134_000).toISOString();
const finishedTime = new Date(previewNow).toISOString();
const previewMarkdownImage = "preview://agent-markdown";

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
    workspaceId: workspaceId,
    projectId: projectId,
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
    isRunning: false,
    hasRunIssue: false,
    lastAgentMessageSeq: 2,
    pendingInteractiveId: null,
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
    finishedAt: ["completed", "failed", "canceled"].includes(status) ? finishedTime : null,
    errorCode: null,
    errorMessage: null,
    timeline: [
      { sequence: 1, type: "item/completed", payload: { item: { id: `commentary-${index}-1`,
        type: "agentMessage", phase: "commentary",
        text: "我先检查现有导航和会话数据边界，再确认哪些组件可以直接复用。" } }, occurredAt: baseTime },
      { sequence: 2, type: "item/completed", payload: { item: { id: `command-${index}`,
        type: "commandExecution", command: "rg -n \"ConversationPane|SessionList\" client", status: "completed" } },
        occurredAt: baseTime },
      { sequence: 3, type: "item/completed", payload: { item: { id: `commentary-${index}-2`,
        type: "agentMessage", phase: "commentary",
        text: "列表和详情已经统一。接下来调整交互，并检查断网恢复是否符合预期。" } },
        occurredAt: baseTime },
      { sequence: 4, type: "item/completed", payload: { item: { id: `files-${index}`,
        type: "fileChange", changes: [{ path: "client/src/features/chat/ConversationPane.tsx" }],
        status: "completed" } }, occurredAt: baseTime },
    ],
    pendingInteractives: [],
  };
}

const runningSession = session(1, "运行中：完善移动端会话体验", { isRunning: true });
const planSession = session(2, "计划已完成：优化消息恢复", { collaborationMode: "plan" });
const interactiveSession = session(3, "等待回答：确认实现范围", {
  pendingInteractiveId: "50000000-0000-4000-8000-000000000003",
});
const secretSession = session(4, "等待桌面端输入敏感信息");
const failedSession = session(5, "执行失败：依赖服务不可用", {
  serviceTier: "standard", hasRunIssue: true,
});
const attachmentSession = session(6, "附件与 Markdown 完整展示");
const archivedSession = session(7, "已归档：旧版通知链路", { lifecycleState: "archived" });
const markdownSession = session(9, "Markdown 排版与长内容验收");
const stressSession = session(10, "性能压测：20 轮 × 3 段 × 60 项动态", {
  lastMessageSeq: 80,
});

const runningRun = run(1, "running");
const planRun = run(2, "completed", "plan");
const interactiveRun = run(3, "waiting_for_user");
interactiveRun.pendingInteractives = [{
  id: "70000000-0000-4000-8000-000000000001",
  status: "pending",
  secret: false,
  deadlineAt: null,
  questions: [{ id: "scope", header: "实现范围", question: "首版优先覆盖哪一项？", options: [
    { label: "完整交互", description: "覆盖会话、计划、问答、停止与附件。" },
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
const markdownRun = run(9, "completed");
markdownRun.timeline = [
  { sequence: 1, type: "item/completed", payload: { item: { id: "markdown-commentary-1",
    type: "agentMessage", phase: "commentary",
    text: "我会先检查 **Markdown 容器宽度** 与 `lineHeight`，确保中文长段落、行内代码和强调文本不会重叠。\n\n> 这段引用用于验证中间过程卡片内的排版。" } }, occurredAt: baseTime },
  { sequence: 2, type: "item/completed", payload: { item: { id: "markdown-command-1",
    type: "commandExecution", command: "pnpm typecheck && pnpm lint", status: "completed" } },
    occurredAt: baseTime },
  { sequence: 3, type: "item/completed", payload: { item: { id: "markdown-files-1",
    type: "fileChange", changes: [{ path: "client/src/features/chat/MarkdownContent.tsx" },
      { path: "client/src/features/chat/RunCards.tsx" }], status: "completed" } }, occurredAt: baseTime },
  { sequence: 4, type: "item/completed", payload: { item: { id: "markdown-commentary-2",
    type: "agentMessage", phase: "commentary",
    text: "排版节点已经统一：\n\n```tsx\n<MarkdownContent compact>{commentary}</MarkdownContent>\n```\n\n接下来核对最终回答；它会保持无卡片正文。" } }, occurredAt: baseTime },
];

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
const agentImageAttachment = {
  id: "60000000-0000-4000-8000-000000000003", sessionId: attachmentSession.id,
  kind: "image" as const, filename: "agent-generated-preview.png", mediaType: "image/png", sizeBytes: 10240,
  sha256: "preview-agent-image", status: "attached" as const, createdAt: baseTime,
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

function stressDetail(): PreviewSessionDetail {
  const turns: ConversationTurn[] = [];
  const activities: Record<string, RunActivity[]> = {};
  const allMessages: Message[] = [];
  for (let turnIndex = 1; turnIndex <= 20; turnIndex++) {
    const turnId = `a1000000-0000-4000-8000-${String(turnIndex).padStart(12, "0")}`;
    const runId = `a2000000-0000-4000-8000-${String(turnIndex).padStart(12, "0")}`;
    const messages = Array.from({ length: 4 }, (_, messageIndex) => {
      const seq = (turnIndex - 1) * 4 + messageIndex + 1;
      const role = messageIndex === 3 ? "agent" as const : "user" as const;
      const labels = [
        `第 ${turnIndex} 轮：分析复杂页面的渲染与滚动性能。`,
        `第 ${turnIndex} 轮 steer：继续检查嵌套列表与 Markdown。`,
        `第 ${turnIndex} 轮交互回答：保留完整活动历史。`,
        `第 ${turnIndex} 轮完成，已记录布局、网络和帧耗时。`,
      ];
      return { ...message(stressSession.id, seq, role, labels[messageIndex]!),
        conversationTurnId: turnId };
    });
    allMessages.push(...messages);
    const segments = Array.from({ length: 3 }, (_, segmentIndex) => {
      const segmentId = `a3000000-${String(turnIndex).padStart(4, "0")}-4000-8${String(segmentIndex)
        .padStart(3, "0")}-${String(turnIndex * 10 + segmentIndex).padStart(12, "0")}`;
      const start = segmentIndex * 60 + 1;
      activities[segmentId] = Array.from({ length: 60 }, (_, activityIndex) => {
        const sequence = start + activityIndex;
        const commentary = activityIndex % 2 === 0;
        return {
          id: `a4000000-${String(turnIndex).padStart(4, "0")}-4${String(segmentIndex).padStart(3, "0")}-8${String(activityIndex).padStart(3, "0")}-${String(sequence).padStart(12, "0")}`,
          itemId: `stress-${turnIndex}-${segmentIndex}-${activityIndex}`,
          kind: commentary ? "commentary" as const : "operation" as const,
          firstEventSequence: sequence,
          lastEventSequence: sequence,
          status: "completed" as const,
          payload: commentary ? { text: `### 步骤 ${activityIndex + 1}\n\n正在分析第 **${turnIndex}** 轮第 \`${segmentIndex + 1}\` 段。这里包含较长的 Markdown 文本、行内代码和列表，用于模拟真实 commentary 的排版成本。\n\n- 检查列表复用\n- 检查高度测量\n- 检查滚动帧` } :
            { item: { id: `command-${turnIndex}-${segmentIndex}-${activityIndex}`,
              type: "commandExecution", command: `rg -n \"stress-${activityIndex}\" client/src`,
              status: "completed" }, eventType: "item/completed" },
          occurredAt: baseTime,
        };
      });
      return {
        id: segmentId,
        sequence: segmentIndex,
        triggerType: segmentIndex === 0 ? "initial" as const :
          segmentIndex === 1 ? "steer" as const : "interactive" as const,
        triggerMessageId: messages[segmentIndex]!.id,
        interactiveRequestId: segmentIndex === 2 ?
          `a5000000-0000-4000-8000-${String(turnIndex).padStart(12, "0")}` : null,
        startEventSequence: start,
        endEventSequence: start + 59,
        activityCount: 60,
      };
    });
    turns.push({ kind: "turn", id: turnId, anchorSeq: messages[0]!.seq, messages, runs: [{
      id: runId,
      attempt: 1,
      status: "completed",
      actualSettings: { model: "gpt-5.6-sol", reasoningEffort: "high", serviceTier: "fast",
        collaborationMode: "default", settingsVersion: 3 },
      startedAt: baseTime,
      finishedAt: finishedTime,
      errorCode: null,
      errorMessage: null,
      segments,
      pendingInteractives: [],
    }] });
  }
  return { settings, currentRun: null, messages: allMessages, turns, activities };
}

const details: Record<string, PreviewSessionDetail> = {
  [runningSession.id]: conversation(runningSession,
    "正在检查 **导航、断网恢复和输入框**。\n\n```tsx\n<ChatComposer mode=\"preview\" />\n```", runningRun),
  [planSession.id]: conversation(planSession,
    "## 执行计划\n\n1. 统一消息状态\n2. 补齐断线恢复\n3. 验证重复发送保护", planRun),
  [interactiveSession.id]: conversation(interactiveSession, "需要确认两个问题后继续。", interactiveRun),
  [secretSession.id]: conversation(secretSession, "下一步需要敏感信息，请在 Codex 桌面端完成。", secretRun),
  [failedSession.id]: conversation(failedSession, "任务未能完成，错误详情见运行进度。", failedRun),
  [attachmentSession.id]: {
    ...conversation(attachmentSession,
      "已收到截图和报告。\n\n- 图片用于检查遮挡与越界\n- 报告用于核对验收结论"),
    messages: [
      message(attachmentSession.id, 1, "user", "请根据附件验收界面。", [imageAttachment, fileAttachment]),
      message(attachmentSession.id, 2, "agent",
        "## 验收摘要\n\n这是正文原位置的 Markdown 图片：\n\n![agent Markdown 图片](" +
        previewMarkdownImage + ")\n\n下方是 agent 原生图片附件。", [agentImageAttachment]),
    ],
  },
  [markdownSession.id]: conversation(markdownSession,
    "# Markdown 排版验收\n\n" +
    "## 字体与行距\n\n" +
    "普通段落包含 **粗体**、*斜体*、~~删除线~~、`inline code` 和 [可点击链接](https://example.com)。这是一段刻意加长的中文内容，用于确认小屏幕会自然换行，连续多行之间保持稳定行距，不会出现文字互相覆盖或越出内容区域。\n\n" +
    "### 引用\n\n> 多行引用的第一行，用来说明验收背景。\n> 第二行仍应位于同一个引用区域，并与正文保持清楚层级。\n\n" +
    "### 列表\n\n- 无序列表第一项\n  - 嵌套项目包含较长说明，用于检查缩进后的可用宽度\n- 无序列表第二项\n\n" +
    "1. 有序列表第一项\n2. 有序列表第二项\n   1. 嵌套编号项目\n\n" +
    "---\n\n### 代码块\n\n```ts\ntype Session = {\n  id: string;\n  title: string;\n};\n\nconst message = \"一行较长但必须正确换行的代码内容\";\n```\n\n" +
    "### 表格\n\n| 状态 | 说明 |\n| --- | --- |\n| running | 正在处理 |\n| completed | 已完成 |\n\n" +
    "长路径也不能越界：`/var/lib/tyrs-hand/workspaces/WakeQora/console-server/src/wakeqora_console/app/settings.py:411`\n\n" +
    "最终回答保持普通正文布局，不增加头像和外层卡片。", markdownRun),
  [archivedSession.id]: conversation(archivedSession, "这条会话已经归档，可随时恢复。"),
  [stressSession.id]: stressDetail(),
};
details[runningSession.id]!.messages = details[runningSession.id]!.messages.filter((item) => item.role !== "agent");

const modelCatalog: Bootstrap["modelCatalogs"][string] = { data: [{
  id: "gpt-5.6-sol",
  model: "gpt-5.6-sol",
  displayName: "GPT-5.6 Sol",
  description: "Latest frontier agentic coding model.",
  inputModalities: ["text", "image", "file"],
  supportedReasoningEfforts: ["low", "medium", "high", "xhigh", "max", "ultra"].map((id) => ({
    reasoningEffort: id,
    description: `${id} 推理强度`,
  })),
  defaultReasoningEffort: "high",
  serviceTiers: [{ id: "priority", name: "Fast", description: "更低延迟的优先处理" }],
  additionalSpeedTiers: ["fast"],
  defaultServiceTier: null,
  isDefault: true,
  hidden: false,
}, {
  id: "gpt-5.6-luna",
  model: "gpt-5.6-luna",
  displayName: "GPT-5.6 Luna",
  description: "Fast and affordable agentic coding model.",
  inputModalities: ["text", "image"],
  supportedReasoningEfforts: [{ reasoningEffort: "medium", description: "平衡速度与质量" },
    { reasoningEffort: "high", description: "处理更复杂的任务" },
    { reasoningEffort: "max", description: "最大推理强度" }],
  defaultReasoningEffort: "medium",
  serviceTiers: [{ id: "priority", name: "Fast", description: "适合大量简单任务" }],
  additionalSpeedTiers: ["fast"],
  defaultServiceTier: "priority",
  isDefault: false,
  hidden: false,
}], nextCursor: null };

const primaryBootstrap: Bootstrap = {
  serverId: primaryPreviewServerId,
  protocolVersion: 3,
  currentCursor: 128,
  user: { id: "80000000-0000-4000-8000-000000000001", username: "UI 验收员" },
  capabilities: { attachments: true, interactive: true, plan: true, push: true, multiControl: true },
  projects: [{
    id: projectId,
    workspaceId,
    name: "Tyrs Hand 移动客户端",
    relativePath: "workspaces/tyrs-hand/client",
    kind: "git",
    availabilityStatus: "available",
    branch: "feature/mobile-preview",
    dirty: true,
  }, {
    id: unavailableProjectId,
    workspaceId,
    name: "名称很长的离线项目，用于验证窄屏换行和边界处理",
    relativePath: "workspaces/archive/a-very-long-project-path-for-layout-acceptance",
    kind: "git",
    availabilityStatus: "offline",
    branch: null,
    dirty: false,
  }],
  agentProfiles: [{ id: profileId, name: "Codex 默认" },
    { id: "20000000-0000-4000-8000-000000000006", name: "Luna Agent" }],
  modelCatalogs: { [workspaceId]: modelCatalog },
  lastStartedSettings: settings,
};

const secondaryBootstrap: Bootstrap = {
  ...primaryBootstrap,
  serverId: secondaryPreviewServerId,
  currentCursor: 12,
  user: { id: "80000000-0000-4000-8000-000000000002", username: "远程开发者" },
  projects: [{
    id: secondaryProjectId,
    workspaceId: "20000000-0000-4000-8000-000000000007",
    name: "远程开发示例",
    relativePath: "workspaces/host-worker",
    kind: "git",
    availabilityStatus: "available",
    branch: "main",
    dirty: false,
  }],
  modelCatalogs: { "20000000-0000-4000-8000-000000000007": modelCatalog },
};

const secondarySession = session(8, "另一连接中的独立会话", {
  projectId: secondaryProjectId,
  workspaceId: secondaryBootstrap.projects[0]!.workspaceId,
});

export function createPreviewSeed(): PreviewSeed {
  const seed: PreviewSeed = {
    connections: [{
      serverId: primaryPreviewServerId,
      baseUrl: "preview://local-control",
      name: "本机服务 · UI 全状态",
      deviceId: "preview-device-primary",
      active: true,
    }, {
      serverId: secondaryPreviewServerId,
      baseUrl: "preview://remote-control-with-a-long-hostname",
      name: "远程服务 · 数据隔离",
      deviceId: "preview-device-secondary",
      active: false,
    }],
    controls: {
      [primaryPreviewServerId]: {
        bootstrap: primaryBootstrap,
        sessions: [runningSession, planSession, interactiveSession, secretSession,
          failedSession, attachmentSession, markdownSession, stressSession, archivedSession],
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
            "这个会话只存在于远程连接，用于验证数据和导航互不混淆。"),
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
  markdown: markdownSession.id,
  stress: stressSession.id,
  archived: archivedSession.id,
};
