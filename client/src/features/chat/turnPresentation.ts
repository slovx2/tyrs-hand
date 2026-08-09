import type { ThreadItem } from "@codex-app-server/v2/ThreadItem";
import type { Turn } from "@codex-app-server/v2/Turn";

type UserItem = Extract<ThreadItem, { type: "userMessage" }>;
type AgentItem = Extract<ThreadItem, { type: "agentMessage" }>;
type PlanItem = Extract<ThreadItem, { type: "plan" }>;
type ReasoningItem = Extract<ThreadItem, { type: "reasoning" }>;
export type ToolItem = Exclude<ThreadItem,
  UserItem | AgentItem | PlanItem | ReasoningItem | Extract<ThreadItem, { type: "hookPrompt" }>>;

export type ToolGroupCategory = "command" | "file" | "search" | "image" |
  "collaboration" | "mcp" | "dynamic" | "wait" | "context" | "review" | "mixed";

export type ToolGroup = {
  kind: "tools";
  key: string;
  items: ToolItem[];
  category: ToolGroupCategory;
  running: boolean;
  inferredRunning: boolean;
  failed: boolean;
  title: string;
};

export type TurnBlock =
  | { kind: "user"; key: string; item: UserItem }
  | { kind: "commentary"; key: string; item: AgentItem }
  | { kind: "reasoning"; key: string; item: ReasoningItem; heading: string }
  | { kind: "final"; key: string; item: AgentItem }
  | { kind: "plan"; key: string; item: PlanItem }
  | ToolGroup;

export type TurnPresentation = {
  blocks: TurnBlock[];
  hasActivity: boolean;
  hasFinalContent: boolean;
  canCollapseActivity: boolean;
};

export type ToolOperation = { key: string; text: string; running: boolean; failed: boolean };

export function projectTurnPresentation(turn: Turn): TurnPresentation {
  const blocks: TurnBlock[] = [];
  let pendingTools: ToolItem[] = [];
  let trailingReasoning: TurnBlock & { kind: "reasoning" } | null = null;
  const unknownFinalId = turn.status === "completed"
    ? [...turn.items].reverse().find((item) =>
      item.type === "agentMessage" && item.phase === null)?.id ?? null
    : null;

  const flushTools = (trailing = false) => {
    if (pendingTools.length === 0) return;
    blocks.push(createToolGroup(pendingTools, trailing && turn.status === "inProgress"));
    pendingTools = [];
  };

  for (const item of turn.items) {
    if (item.type === "reasoning") {
      const heading = reasoningActivityHeading(item.summary);
      trailingReasoning = heading
        ? { kind: "reasoning", key: item.id, item, heading }
        : trailingReasoning;
      continue;
    }
    if (isToolItem(item)) {
      pendingTools.push(item);
      continue;
    }
    trailingReasoning = null;
    flushTools();
    if (item.type === "userMessage") {
      blocks.push({ kind: "user", key: item.id, item });
    } else if (item.type === "agentMessage") {
      blocks.push({ kind: item.phase === "final_answer" || item.id === unknownFinalId
        ? "final" : "commentary", key: item.id, item });
    } else if (item.type === "plan") {
      blocks.push({ kind: "plan", key: item.id, item });
    }
  }
  flushTools(true);
  // 官方只把最新 reasoning 当作当前思考状态；完成后的历史 reasoning 不逐条回放。
  // reasoning 也不会切断可分组工具，因此长任务不会退化成大量单工具披露行。
  const trailingBlock = blocks.at(-1);
  const trailingToolIsRunning = trailingBlock?.kind === "tools" && trailingBlock.running;
  if (turn.status === "inProgress" && trailingReasoning && !trailingToolIsRunning) {
    blocks.push(trailingReasoning);
  }

  const hasActivity = blocks.some((block) => block.kind === "tools" ||
    block.kind === "reasoning" || block.kind === "commentary" && block.item.text.trim() !== "");
  const hasFinalContent = blocks.some((block) =>
    block.kind === "plan" && block.item.text.trim() !== "" ||
    block.kind === "final" && block.item.text.trim() !== "");
  return { blocks, hasActivity, hasFinalContent,
    canCollapseActivity: hasActivity && hasFinalContent && turn.status !== "interrupted" };
}

/**
 * 官方 reasoning summary 会用独占的 Markdown 粗体行表达活动标题。
 * 移动端只展示这个标题，避免把 `**...**` 当作普通文本泄漏到活动流中。
 */
export function reasoningActivityHeading(summary: string[]): string | null {
  const lines = summary.flatMap((part) => part.split(/\r?\n/))
    .map((line) => line.trim()).filter(Boolean);
  if (lines.length === 0) return null;

  for (let index = lines.length - 1; index >= 0; index -= 1) {
    const bold = lines[index]!.match(/^\*\*(.+?)\*\*$/);
    if (bold?.[1]?.trim()) return cleanReasoningHeading(bold[1]);
  }
  const markdownHeading = lines[0]!.match(/^#{1,3}\s+(.+)$/);
  return cleanReasoningHeading(markdownHeading?.[1] ?? lines.at(-1)!);
}

function cleanReasoningHeading(value: string): string | null {
  const cleaned = value.trim()
    .replace(/^\*\*(.+)\*\*$/, "$1")
    .replace(/^__(.+)__$/, "$1")
    .replace(/^`(.+)`$/, "$1")
    .replace(/\[([^\]]+)\]\([^\s)]+\)/g, "$1")
    .trim();
  return cleaned || null;
}

export function createToolGroup(items: ToolItem[], inferStatelessRunning = false): ToolGroup {
  const category = toolGroupCategory(items);
  const explicitlyRunning = items.some(isToolRunning);
  const inferredRunning = !explicitlyRunning && inferStatelessRunning &&
    items.some((item) => item.type === "webSearch");
  const running = explicitlyRunning || inferredRunning;
  const failed = items.some(isToolFailed);
  return { kind: "tools", key: items[0]!.id, items: [...items], category, running,
    inferredRunning, failed, title: category === "mixed"
      ? mixedToolGroupTitle(items, running, failed)
      : toolGroupTitle(category, running, failed) };
}

/**
 * 官方完成态不会只显示笼统的 “Used tools”，而会在折叠头汇总工具类别和数量。
 * 移动端不展示工具输出，因此这个摘要也是用户无需二次展开即可确认工具调用的入口。
 */
export function mixedToolGroupTitle(items: ToolItem[], running: boolean,
  failed: boolean): string {
  if (failed && !running) return "工具调用失败";
  const counts = new Map<ToolGroupCategory, number>();
  for (const item of items) {
    const category = toolItemCategory(item);
    const count = category === "file" && item.type === "fileChange"
      ? Math.max(1, item.changes.length) : 1;
    counts.set(category, (counts.get(category) ?? 0) + count);
  }
  const parts = TOOL_SUMMARY_ORDER.flatMap((category) => {
    const count = counts.get(category);
    return count === undefined ? [] : [toolCategorySummary(category, count, running)];
  });
  return parts.join("、") || toolGroupTitle("mixed", running, failed);
}

export function toolGroupTitle(category: ToolGroupCategory, running: boolean,
  failed: boolean): string {
  const labels: Record<ToolGroupCategory, [string, string, string]> = {
    command: ["正在运行命令", "运行了命令", "运行命令失败"],
    file: ["正在修改文件", "修改了文件", "修改文件失败"],
    search: ["正在搜索网页", "搜索了网页", "搜索网页失败"],
    image: ["正在处理图片", "处理了图片", "图片操作失败"],
    collaboration: ["正在协调协作任务", "协调了协作任务", "协作任务失败"],
    mcp: ["正在调用 MCP 工具", "调用了 MCP 工具", "MCP 工具调用失败"],
    dynamic: ["正在调用动态工具", "调用了动态工具", "动态工具调用失败"],
    wait: ["正在等待", "完成了等待", "等待失败"],
    context: ["正在压缩会话上下文", "压缩了会话上下文", "压缩上下文失败"],
    review: ["正在进行代码审查", "完成了代码审查", "代码审查操作失败"],
    mixed: ["正在使用工具", "使用了工具", "工具调用失败"],
  };
  return labels[category][failed && !running ? 2 : running ? 0 : 1];
}

const TOOL_SUMMARY_ORDER: ToolGroupCategory[] = [
  "mcp", "file", "context", "command", "search", "image", "collaboration", "dynamic",
  "wait", "review",
];

function toolCategorySummary(category: ToolGroupCategory, count: number,
  running: boolean): string {
  if (running) {
    const labels: Record<ToolGroupCategory, string> = {
      command: "正在运行命令",
      file: "正在修改文件",
      search: "正在搜索网页",
      image: "正在处理图片",
      collaboration: "正在协调协作任务",
      mcp: "正在调用 MCP 工具",
      dynamic: "正在调用动态工具",
      wait: "正在等待",
      context: "正在压缩上下文",
      review: "正在审查代码",
      mixed: "正在使用工具",
    };
    return labels[category];
  }
  const labels: Record<ToolGroupCategory, string> = {
    command: `运行了 ${count} 条命令`,
    file: `修改了 ${count} 个文件`,
    search: `搜索了 ${count} 次网页`,
    image: `处理了 ${count} 项图片`,
    collaboration: `协调了 ${count} 项协作任务`,
    mcp: `调用了 ${count} 个 MCP 工具`,
    dynamic: `调用了 ${count} 个动态工具`,
    wait: `等待了 ${count} 次`,
    context: `压缩了 ${count} 次上下文`,
    review: `进行了 ${count} 次代码审查`,
    mixed: `使用了 ${count} 个工具`,
  };
  return labels[category];
}

export function toolOperationLines(item: ToolItem, inferStatelessRunning = false): ToolOperation[] {
  const running = isToolRunning(item) || inferStatelessRunning && item.type === "webSearch";
  const failed = isToolFailed(item);
  switch (item.type) {
  case "commandExecution":
    return [operation(item.id, `${actionPrefix("运行", running, failed)} ${item.command}`,
      running, failed)];
  case "fileChange":
    return item.changes.length > 0
      ? item.changes.map((change, index) => operation(`${item.id}:${index}`,
        `${actionPrefix("修改", running, failed)} ${change.path}`, running, failed))
      : [operation(item.id, actionPrefix("修改文件", running, failed), running, failed)];
  case "mcpToolCall":
    return [operation(item.id, `${actionPrefix("调用", running, failed)} ${item.server} · ${item.tool}`,
      running, failed)];
  case "dynamicToolCall":
    return [operation(item.id,
      `${actionPrefix("调用", running, failed)} ${item.namespace ? `${item.namespace} · ` : ""}${item.tool}`,
      running, failed)];
  case "webSearch":
    return [operation(item.id, webSearchOperation(item, running), running, failed)];
  case "imageView":
    return [operation(item.id, `已查看 ${item.path}`, running, failed)];
  case "imageGeneration":
    return [operation(item.id, actionPrefix("生成图片", running, failed), running, failed)];
  case "collabAgentToolCall":
    return [operation(item.id, collabOperation(item.tool, running, failed), running, failed)];
  case "subAgentActivity":
    return [operation(item.id, subAgentOperation(item.kind), running, failed)];
  case "sleep":
    return [operation(item.id, `${running ? "正在等待" : "已等待"} ${formatDuration(item.durationMs)}`,
      running, failed)];
  case "contextCompaction":
    return [operation(item.id, "已压缩会话上下文", running, failed)];
  case "enteredReviewMode":
    return [operation(item.id, "已开始代码审查", running, failed)];
  case "exitedReviewMode":
    return [operation(item.id, "已完成代码审查", running, failed)];
  }
}

export function turnWorkedDurationMs(turn: Turn, nowMs = Date.now()): number | null {
  if (turn.durationMs !== null) return Math.max(0, turn.durationMs);
  if (turn.startedAt === null) return null;
  const endMs = turn.completedAt === null ? nowMs : turn.completedAt * 1000;
  return Math.max(0, endMs - turn.startedAt * 1000);
}

export function turnActivitySummary(turn: Turn, nowMs = Date.now()): string {
  const duration = turnWorkedDurationMs(turn, nowMs);
  return duration === null ? "处理过程" : `耗时 ${formatDuration(duration)}`;
}

export function formatDuration(durationMs: number): string {
  const seconds = Math.max(1, Math.round(durationMs / 1000));
  if (seconds < 60) return `${seconds}秒`;
  const minutes = Math.floor(seconds / 60);
  const remainingSeconds = seconds % 60;
  if (minutes < 60) return remainingSeconds === 0
    ? `${minutes}分钟` : `${minutes}分钟 ${remainingSeconds}秒`;
  const hours = Math.floor(minutes / 60);
  const remainingMinutes = minutes % 60;
  return remainingMinutes === 0 ? `${hours}小时` : `${hours}小时 ${remainingMinutes}分钟`;
}

export function isToolRunning(item: ToolItem): boolean {
  return "status" in item && (item.status === "inProgress" || item.status === "generating");
}

export function isToolFailed(item: ToolItem): boolean {
  if (item.type === "dynamicToolCall" && item.success === false) return true;
  return "status" in item && (item.status === "failed" || item.status === "declined");
}

function isToolItem(item: ThreadItem): item is ToolItem {
  return item.type !== "userMessage" && item.type !== "agentMessage" && item.type !== "plan" &&
    item.type !== "reasoning" && item.type !== "hookPrompt";
}

function toolGroupCategory(items: ToolItem[]): ToolGroupCategory {
  const categories = new Set(items.map(toolItemCategory));
  return categories.size === 1 ? [...categories][0]! : "mixed";
}

function toolItemCategory(item: ToolItem): ToolGroupCategory {
  switch (item.type) {
  case "commandExecution": return "command";
  case "fileChange": return "file";
  case "webSearch": return "search";
  case "imageView":
  case "imageGeneration": return "image";
  case "collabAgentToolCall":
  case "subAgentActivity": return "collaboration";
  case "mcpToolCall": return "mcp";
  case "dynamicToolCall": return "dynamic";
  case "sleep": return "wait";
  case "contextCompaction": return "context";
  case "enteredReviewMode":
  case "exitedReviewMode": return "review";
  }
}

function webSearchOperation(item: Extract<ToolItem, { type: "webSearch" }>, running: boolean): string {
  if (item.action?.type === "openPage" && item.action.url) {
    return `${running ? "正在打开" : "已打开"} ${item.action.url}`;
  }
  if (item.action?.type === "findInPage") {
    const target = item.action.url ? ` ${item.action.url}` : "页面";
    return `${running ? "正在" : "已"}在${target}中查找 ${item.action.pattern ?? "内容"}`;
  }
  const query = item.action?.type === "search"
    ? item.action.query ?? item.action.queries?.join("、") ?? item.query : item.query;
  const prefix = running ? "正在搜索" : "已搜索";
  return query ? `${prefix} ${query}` : `${prefix}网页`;
}

function collabOperation(tool: Extract<ToolItem, { type: "collabAgentToolCall" }>["tool"],
  running: boolean, failed: boolean): string {
  const prefix = failed ? "未能" : running ? "正在" : "已";
  switch (tool) {
  case "spawnAgent": return `${prefix}启动协作任务`;
  case "sendInput": return `${prefix}向协作任务发送消息`;
  case "resumeAgent": return `${prefix}恢复协作任务`;
  case "wait": return `${prefix}等待协作任务`;
  case "closeAgent": return `${prefix}关闭协作任务`;
  }
}

function subAgentOperation(kind: Extract<ToolItem, { type: "subAgentActivity" }>["kind"]): string {
  switch (kind) {
  case "started": return "已启动协作任务";
  case "interacted": return "已更新协作任务";
  case "interrupted": return "已中断协作任务";
  }
}

function actionPrefix(action: string, running: boolean, failed: boolean): string {
  return failed ? `${action}失败` : running ? `正在${action}` : `已${action}`;
}

function operation(key: string, text: string, running: boolean,
  failed: boolean): ToolOperation {
  return { key, text, running, failed };
}
