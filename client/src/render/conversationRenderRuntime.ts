import { createWorkletRuntime, runOnRuntime, scheduleOnRN } from "react-native-worklets";

export type RenderInputItem = {
  id: string;
  type: string;
  text?: string;
  phase?: string | null;
  status?: string;
  [key: string]: unknown;
};

export type RenderInput = {
  turnId: string;
  status: string;
  startedAt: number | null;
  completedAt: number | null;
  durationMs: number | null;
  items: RenderInputItem[];
};

export type RenderModelBlock = {
  kind: "user" | "commentary" | "final" | "plan" | "tools";
  key: string;
  item?: RenderInputItem;
  items?: RenderInputItem[];
  category?: string;
  title?: string;
  running?: boolean;
};

export type TurnRenderModel = {
  turnId: string;
  blocks: RenderModelBlock[];
  hasActivity: boolean;
  hasFinalContent: boolean;
  canCollapseActivity: boolean;
  showThinking: boolean;
  thinkingLabel: string | null;
};

type Complete = (requestId: number, generation: number,
  result: TurnRenderModel | null, error: string | null) => void;

const runtime = createWorkletRuntime({ name: "conversation-render", useDefaultQueue: true });
const pending = new Map<number, { generation: number; resolve: (result: TurnRenderModel) => void;
  reject: (error: Error) => void }>();
let nextRequestId = 0;

function projectTurn(input: RenderInput): TurnRenderModel {
  "worklet";
  const blocks: RenderModelBlock[] = [];
  let tools: RenderInputItem[] = [];
  let heading: string | null = null;
  let hasDynamicImage = false;
  for (const item of input.items) {
    if (item.type === "dynamicToolCall" && item.tool === "generate_image" && item.success === true) {
      hasDynamicImage = true;
    }
  }
  let finalId: string | null = null;
  if (input.status === "completed") {
    for (let index = input.items.length - 1; index >= 0; index -= 1) {
      const item = input.items[index];
      if (item?.type === "agentMessage" && item.phase == null) {
        finalId = item.id;
        break;
      }
    }
  }
  const flush = (trailing = false) => {
    if (tools.length === 0) return;
    const category = toolCategory(tools);
    const running = tools.some((item) => item.status === "inProgress" || item.status === "generating") ||
      trailing && input.status === "inProgress";
    blocks.push({ kind: "tools", key: tools[0]!.id, items: tools.slice(), category,
      running, title: heading ?? toolTitle(category, running) });
    tools = [];
  };
  for (const item of input.items) {
    if (item.type === "reasoning") {
      const summary = typeof item.summary === "string" ? item.summary.trim() : "";
      if (summary) heading = cleanHeading(summary);
      continue;
    }
    if (isTool(item)) {
      tools.push(item);
      continue;
    }
    flush();
    heading = null;
    if (item.type === "userMessage") blocks.push({ kind: "user", key: item.id, item });
    else if (item.type === "agentMessage") blocks.push({
      kind: item.phase === "final_answer" || item.id === finalId ? "final" : "commentary",
      key: item.id, item: hasDynamicImage ? { ...item, text: stripImages(item.text ?? "") } : item,
    });
    else if (item.type === "plan") blocks.push({ kind: "plan", key: item.id, item });
  }
  flush(true);
  let hasActivity = false;
  let hasFinalContent = false;
  for (const block of blocks) {
    if (block.kind === "tools" || block.kind === "commentary" && (block.item?.text ?? "").trim()) {
      hasActivity = true;
    }
    if (block.kind === "final" || block.kind === "plan") {
      hasFinalContent ||= Boolean((block.item?.text ?? "").trim());
    }
  }
  const last = blocks[blocks.length - 1];
  const activityRunning = last?.kind === "tools" && last.running === true;
  return { turnId: input.turnId, blocks, hasActivity, hasFinalContent,
    canCollapseActivity: hasActivity && hasFinalContent && input.status !== "interrupted",
    showThinking: input.status === "inProgress" && !hasFinalContent && !activityRunning,
    thinkingLabel: input.status === "inProgress" && !hasFinalContent && !activityRunning ? heading : null };
}

function isTool(item: RenderInputItem): boolean {
  return item.type !== "userMessage" && item.type !== "agentMessage" && item.type !== "plan" &&
    item.type !== "reasoning" && item.type !== "hookPrompt";
}

function toolCategory(items: RenderInputItem[]): string {
  let category = "";
  for (const item of items) {
    const next = item.type === "commandExecution" ? "command" : item.type === "fileChange" ? "file" :
      item.type === "webSearch" ? "search" : item.type === "imageView" || item.type === "imageGeneration" ? "image" :
      item.type === "mcpToolCall" ? "mcp" : item.type === "sleep" ? "wait" : "dynamic";
    if (!category) category = next;
    else if (category !== next) return "mixed";
  }
  return category || "mixed";
}

function toolTitle(category: string, running: boolean): string {
  const labels: Record<string, [string, string]> = {
    command: ["正在运行命令", "运行了命令"], file: ["正在修改文件", "修改了文件"],
    search: ["正在搜索网页", "搜索了网页"], image: ["正在处理图片", "处理了图片"],
    mcp: ["正在调用 MCP 工具", "调用了 MCP 工具"], wait: ["正在等待", "完成了等待"],
    dynamic: ["正在调用动态工具", "调用了动态工具"], mixed: ["正在使用工具", "使用了工具"],
  };
  const pair = labels[category] ?? labels.mixed ?? ["正在使用工具", "使用了工具"];
  return pair[running ? 0 : 1] ?? "使用了工具";
}

function cleanHeading(value: string): string {
  return value.split(/\r?\n/).map((line) => line.trim()).filter(Boolean).at(-1)!
    .replace(/^\*\*(.+)\*\*$/, "$1").replace(/^#+\s+/, "").trim();
}

function stripImages(value: string): string {
  return value.replace(/!\[[^\]]*\]\((?:file:\/\/)?\/[^\n)]*\)/g, "").trim();
}

function onComplete(requestId: number, generation: number,
  result: TurnRenderModel | null, error: string | null): void {
  const task = pending.get(requestId);
  if (!task) return;
  pending.delete(requestId);
  if (task.generation !== generation) {
    task.reject(new Error("后台渲染结果已过期"));
    return;
  }
  if (error || !result) task.reject(new Error(error ?? "后台渲染失败"));
  else task.resolve(result);
}

const schedule = runOnRuntime(runtime, (requestId: number, generation: number,
  input: RenderInput, complete: Complete) => {
  "worklet";
  try {
    scheduleOnRN(complete, requestId, generation, projectTurn(input), null);
  } catch (error) {
    scheduleOnRN(complete, requestId, generation, null,
      error instanceof Error ? error.message : "后台渲染失败");
  }
});

export function renderTurnInBackground(input: RenderInput, generation = 0): Promise<TurnRenderModel> {
  const requestId = ++nextRequestId;
  return new Promise<TurnRenderModel>((resolve, reject) => {
    pending.set(requestId, { generation, resolve, reject });
    schedule(requestId, generation, input, onComplete);
  });
}

export function renderInputFromTurn(turn: {
  id: string;
  status: string;
  startedAt: number | null;
  completedAt: number | null;
  durationMs: number | null;
  items: unknown[];
}): RenderInput {
  return { turnId: turn.id, status: turn.status, startedAt: turn.startedAt,
    completedAt: turn.completedAt, durationMs: turn.durationMs,
    items: turn.items.map((value) => renderInputItem(value)),
  };
}

function renderInputItem(value: unknown): RenderInputItem {
  if (!value || typeof value !== "object") {
    return { id: "unknown", type: "unknown" };
  }
  const item = value as Record<string, unknown>;
  const result: RenderInputItem = {
    id: typeof item.id === "string" ? item.id : "unknown",
    type: typeof item.type === "string" ? item.type : "unknown",
  };
  for (const key of ["phase", "status", "tool", "command", "path", "name"]) {
    const field = item[key];
    if (typeof field === "string") result[key] = field;
  }
  if (typeof item.success === "boolean") result.success = item.success;
  if (typeof item.text === "string") result.text = item.text.slice(0, 12_000);
  if (Array.isArray(item.summary)) {
    result.summary = item.summary.filter((part): part is string => typeof part === "string")
      .join("\n").slice(0, 2_000);
  }
  return result;
}
