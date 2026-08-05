import type { RunActivity, RunSnapshot } from "@/types/protocol";

type CommentaryPart = { kind: "commentary"; id: string; text: string };
export type Operation = { id: string; label: string; status: "running" | "completed" | "failed" };
export type OperationsPart = { kind: "operations"; id: string; operations: Operation[] };
export type RunActivityPart = CommentaryPart | OperationsPart;

export function isUnclosedOperationsPart(parts: RunActivityPart[], index: number,
  segmentActive: boolean, hasFinalAnswer: boolean): boolean {
  return segmentActive && !hasFinalAnswer && index === parts.length - 1 &&
    parts[index]?.kind === "operations";
}

function record(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" ? value as Record<string, unknown> : {};
}

function string(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function compact(value: string, max = 64): string {
  const normalized = value.replace(/\s+/g, " ").trim();
  return normalized.length > max ? `${normalized.slice(0, max - 1)}…` : normalized;
}

function operationLabel(type: string, item: Record<string, unknown>, eventType: string): string {
  const status = eventType.endsWith("completed") ? "已" : "正在";
  if (type === "commandExecution") return `${status}运行命令 ${compact(string(item.command) || string(item.cmd))}`.trim();
  if (type === "fileChange") {
    const changes = Array.isArray(item.changes) ? item.changes.map((value) =>
      string(record(value).path)).filter(Boolean) : [];
    return `${status}修改文件${changes.length ? ` ${compact(changes.join("、"))}` : ""}`;
  }
  if (["mcpToolCall", "dynamicToolCall"].includes(type)) {
    const namespace = string(item.server) || string(item.namespace);
    const tool = [namespace, string(item.tool) || string(item.name)].filter(Boolean).join(".");
    return `${status}调用${tool ? ` ${tool}` : "工具"}`;
  }
  if (type === "webSearch") return `${status}搜索 ${compact(string(item.query))}`.trim();
  if (type === "collabAgentToolCall") return `${status}调度子 Agent`;
  if (type) return `${status}处理任务`;
  return eventType ? `${status}处理任务` : "处理任务";
}

export function buildRunActivity(run: RunSnapshot): RunActivityPart[] {
  const parts: RunActivityPart[] = [];
  const commentaryIndexes = new Map<string, number>();
  const appendCommentary = (id: string, text: string, replace: boolean) => {
    if (!id || !text) return;
    const existing = commentaryIndexes.get(id);
    if (existing !== undefined) {
      const part = parts[existing];
      if (part?.kind === "commentary") part.text = replace ? text.trim() : part.text + text;
      return;
    }
    commentaryIndexes.set(id, parts.length);
    parts.push({ kind: "commentary", id, text: text.trimStart() });
  };
  const appendOperation = (operation: Operation) => {
    for (const part of parts) {
      if (part.kind !== "operations") continue;
      const existing = part.operations.findIndex((item) => item.id === operation.id);
      if (existing >= 0) {
        part.operations[existing] = operation;
        return;
      }
    }
    const previous = parts.at(-1);
    if (previous?.kind === "operations") previous.operations.push(operation);
    else parts.push({ kind: "operations", id: `operations-${operation.id}`, operations: [operation] });
  };

  for (const event of run.timeline) {
    const payload = record(event.payload);
    const item = record(payload.item);
    const itemType = string(item.type);
    const phase = string(item.phase) || string(payload.phase);
    const id = string(item.id) || string(payload.itemId) || `event-${event.sequence}`;
    if (itemType === "agentMessage") {
      if (phase === "commentary") appendCommentary(id, string(item.text), true);
      continue;
    }
    if (event.type === "item/agentMessage/delta" || event.type === "item/delta") {
      if (phase === "commentary" || commentaryIndexes.has(id)) {
        appendCommentary(id, string(payload.delta) || string(payload.text), false);
      }
      continue;
    }
    if (event.type === "item/started" || event.type === "item/completed" ||
      event.type === "discord/tool/started" || event.type === "discord/tool/completed") {
      if (!itemType) continue;
      const failed = string(item.status) === "failed" || record(item.error).message !== undefined;
      appendOperation({ id, label: operationLabel(itemType, item, event.type),
        status: failed ? "failed" : event.type.endsWith("completed") ? "completed" : "running" });
      continue;
    }
    if (event.type !== "runtime.settings_applied") appendOperation({ id,
      label: "处理任务", status: "completed" });
  }
  return parts.filter((part) => part.kind === "operations" || part.text.trim() !== "");
}

export function buildProjectedRunActivity(activities: RunActivity[]): RunActivityPart[] {
  const parts: RunActivityPart[] = [];
  for (const activity of activities) {
    const payload = record(activity.payload);
    if (activity.kind === "commentary") {
      const value = string(payload.text);
      if (value.trim()) parts.push({ kind: "commentary", id: activity.id, text: value });
      continue;
    }
    const item = record(payload.item);
    const operation = { id: activity.itemId,
      label: operationLabel(string(item.type), item, string(payload.eventType)), status: activity.status };
    const previous = parts.at(-1);
    if (previous?.kind === "operations") previous.operations.push(operation);
    else parts.push({ kind: "operations", id: `operations-${activity.id}`, operations: [operation] });
  }
  return parts;
}
