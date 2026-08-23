import type { Thread } from "@codex-app-server/v2/Thread";
import type { ThreadItem } from "@codex-app-server/v2/ThreadItem";

import type { OfficialTurnPage } from "@/app-server/officialClient";

type Turns = Thread["turns"];
const turnSnapshotKeys = new WeakMap<Turns[number], string>();

export type MergedTailPage = {
  turns: Turns;
  overlapped: boolean;
};

export type MergedOlderPage = {
  turns: Turns;
  nextCursor: string | null;
  hasLoadedOldest: boolean;
};

export type MergeTurnSnapshotOptions = {
  preserveMissingItems?: boolean;
};

export function mergeTailPage(existing: Turns, incoming: Turns): MergedTailPage {
  const incomingIds = new Set(incoming.map((turn) => turn.id));
  const overlap = existing.findIndex((turn) => incomingIds.has(turn.id));
  if (overlap < 0) {
    return { turns: preserveProvisionalTurns(existing, incoming, incoming), overlapped: false };
  }
  // 最新页可能从很早的 Turn 开始重叠。只保留首个重叠点之前的历史前缀，
  // 但所有仍出现在权威页中的既有 Turn 都要参与快照合并；否则页首先匹配时，
  // 尾部活动 Turn 已观察到的原生工具会被 legacy 短快照直接覆盖。
  const overlappingExisting = existing.slice(overlap)
    .filter((turn) => incomingIds.has(turn.id));
  const merged = mergeTurnSequence(existing.slice(0, overlap), overlappingExisting, incoming);
  // 发送中的乐观 Turn 没有官方 ID，尾页在 turn/started 或 turn/steer
  // 通知到达前可能暂时还看不到它。不要因为一次权威尾页读取就把用户刚发的
  // 消息从列表中删掉；等同一 clientMessageId 出现在官方 Turn 后再自然收敛。
  const withProvisional = preserveProvisionalTurns(existing, incoming, merged);
  return { turns: sameTurnSequence(existing, withProvisional) ? existing : withProvisional,
    overlapped: true };
}

function preserveProvisionalTurns(existing: Turns, incoming: Turns, merged: Turns): Turns {
  const provisional = existing.filter((turn) => turn.id.startsWith("provisional:") &&
    !incoming.some((candidate) => candidate.items.some((item) =>
      item.type === "userMessage" && turn.items.some((previous) =>
        previous.type === "userMessage" && previous.clientId !== null &&
        previous.clientId === item.clientId))));
  return provisional.length > 0 ? mergeTurnSequence(merged, provisional) : merged;
}

export function mergeOlderPage(existing: Turns, currentCursor: string | null,
  requestedCursor: string, page: OfficialTurnPage): MergedOlderPage | null {
  if (currentCursor !== requestedCursor) return null;
  if (page.nextCursor === requestedCursor) {
    throw new Error("thread/turns/list 返回了重复游标");
  }
  return {
    turns: mergeTurnSequence(page.turns, existing),
    nextCursor: page.nextCursor,
    hasLoadedOldest: page.nextCursor === null,
  };
}

export function mergeTurnSequence(...groups: Turns[]): Turns {
  const order: string[] = [];
  const turns = new Map<string, Turns[number]>();
  for (const turn of groups.flat()) {
    if (!turns.has(turn.id)) order.push(turn.id);
    const previous = turns.get(turn.id);
    turns.set(turn.id, previous ? mergeTurnSnapshot(previous, turn) : turn);
  }
  return order.flatMap((id) => turns.get(id) ?? []);
}

/**
 * 官方尾页在 Turn 刚结束时偶尔会短暂缺少已推送过的工具 Item。
 * 同一活动 Turn 因此按 Item ID 单调合并；完成态正文仍以最新官方快照为准。
 */
export function mergeTurnSnapshot(previous: Turns[number], incoming: Turns[number],
  options: MergeTurnSnapshotOptions = {}): Turns[number] {
  if (sameTurnSnapshot(previous, incoming)) return previous;
  const incomingById = new Map(incoming.items.map((item) => [item.id, item]));
  const aliasedIncomingByPreviousId = semanticItemAliases(previous.items, incoming.items);
  const seen = new Set<string>();
  const items: ThreadItem[] = [];
  for (const item of previous.items) {
    const aliased = aliasedIncomingByPreviousId.get(item.id);
    const updated = incomingById.get(item.id) ?? aliased;
    if (updated) {
      items.push(mergeItemSnapshot(item, updated.id === item.id ? updated
        : withItemId(updated, item.id), incoming.status !== "inProgress"));
      seen.add(updated.id);
    } else if (options.preserveMissingItems || incoming.status === "inProgress" ||
      isToolItem(item)) {
      // Turn 已经进入终态时，旧快照里仍是 inProgress/generating 的工具不可能继续运行。
      // 保留调用本身用于还原时间线，但必须先收敛 Item 状态，否则会把错误的运行态写入缓存，
      // 下次启动仍显示 shimmer。
      items.push(incoming.status === "inProgress" ? item : completeMissingTool(item));
      seen.add(item.id);
    }
  }
  for (const item of incoming.items) {
    if (!seen.has(item.id)) items.push(item);
  }
  const merged = { ...incoming, items };
  return sameTurnSnapshot(previous, merged) ? previous : merged;
}

export function normalizeTerminalTurn(turn: Turns[number]): Turns[number] {
  if (turn.status === "inProgress") return turn;
  let changed = false;
  const items = turn.items.map((item) => {
    const next = completeMissingTool(item);
    changed ||= next !== item;
    return next;
  });
  return changed ? { ...turn, items } : turn;
}

/**
 * legacy 尾页会为当前 Turn 重新生成 `item-N`，而原生通知携带真实 Item ID。
 * 两条链路同时到达时按内容和顺序一对一建立别名，避免把同一条用户消息、commentary
 * 或工具调用重复追加。匹配只在类型和稳定语义都一致时发生。
 */
function semanticItemAliases(previous: ThreadItem[], incoming: ThreadItem[]):
  Map<string, ThreadItem> {
  const exactPreviousIds = new Set(previous.map((item) => item.id));
  const consumedPreviousIds = new Set<string>();
  const aliases = new Map<string, ThreadItem>();
  for (const candidate of incoming) {
    if (exactPreviousIds.has(candidate.id)) {
      consumedPreviousIds.add(candidate.id);
      continue;
    }
    const matched = previous.find((item) => !consumedPreviousIds.has(item.id) &&
      semanticallySameItem(item, candidate));
    if (!matched) continue;
    consumedPreviousIds.add(matched.id);
    aliases.set(matched.id, candidate);
  }
  return aliases;
}

function semanticallySameItem(left: ThreadItem, right: ThreadItem): boolean {
  if (left.type !== right.type) return false;
  switch (left.type) {
  case "userMessage":
    if (right.type !== "userMessage") return false;
    if (left.clientId !== null && right.clientId !== null) {
      return left.clientId === right.clientId;
    }
    // Desktop/legacy 恢复的首条 User Item 可能没有 clientId，且每次尾页读取都会
    // 重新生成 item-N。此时只按完整结构化输入一对一匹配，避免同一首条消息重复追加。
    return JSON.stringify(left.content) === JSON.stringify(right.content);
  case "agentMessage":
    return right.type === "agentMessage" && left.phase === right.phase &&
      growingEquivalent(left.text, right.text);
  case "plan":
    return right.type === "plan" && growingEquivalent(left.text, right.text);
  case "reasoning":
    return right.type === "reasoning" && growingPartsEquivalent(left.summary, right.summary) &&
      growingPartsEquivalent(left.content, right.content);
  case "commandExecution":
    return right.type === "commandExecution" && left.command === right.command &&
      left.cwd === right.cwd && left.source === right.source;
  case "fileChange":
    return right.type === "fileChange" && JSON.stringify(left.changes.map((change) =>
      [change.path, change.kind])) === JSON.stringify(right.changes.map((change) =>
      [change.path, change.kind]));
  case "mcpToolCall":
    return right.type === "mcpToolCall" && left.server === right.server &&
      left.tool === right.tool;
  case "dynamicToolCall":
    return right.type === "dynamicToolCall" && left.namespace === right.namespace &&
      left.tool === right.tool;
  case "webSearch":
    return right.type === "webSearch" && left.query === right.query &&
      JSON.stringify(left.action) === JSON.stringify(right.action);
  default:
    return false;
  }
}

function growingEquivalent(left: string, right: string): boolean {
  return left.length > 0 && right.length > 0 &&
    (left.startsWith(right) || right.startsWith(left));
}

function growingPartsEquivalent(left: string[], right: string[]): boolean {
  const leftText = left.join("\n").trim();
  const rightText = right.join("\n").trim();
  return growingEquivalent(leftText, rightText);
}

function withItemId(item: ThreadItem, id: string): ThreadItem {
  return { ...item, id } as ThreadItem;
}

export function mergeItemSnapshot(previous: ThreadItem, incoming: ThreadItem,
  completed: boolean): ThreadItem {
  if (sameItemSnapshot(previous, incoming)) return previous;
  if (completed || previous.type !== incoming.type) return incoming;
  if (previous.type === "agentMessage" && incoming.type === "agentMessage") {
    const text = growingText(previous.text, incoming.text);
    return text === previous.text && previous.phase === incoming.phase ? previous
      : { ...incoming, text };
  }
  if (previous.type === "plan" && incoming.type === "plan") {
    const text = growingText(previous.text, incoming.text);
    return text === previous.text ? previous : { ...incoming, text };
  }
  if (previous.type === "reasoning" && incoming.type === "reasoning") {
    const summary = mergeGrowingParts(previous.summary, incoming.summary);
    const content = mergeGrowingParts(previous.content, incoming.content);
    return summary === previous.summary && content === previous.content ? previous
      : { ...incoming, summary, content };
  }
  return incoming;
}

function growingText(previous: string, incoming: string): string {
  if (incoming.startsWith(previous)) return incoming;
  if (previous.startsWith(incoming)) return previous;
  return incoming.length >= previous.length ? incoming : previous;
}

function mergeGrowingParts(previous: string[], incoming: string[]): string[] {
  const length = Math.max(previous.length, incoming.length);
  const result = Array.from({ length }, (_, index) =>
    growingText(previous[index] ?? "", incoming[index] ?? ""));
  return result.length === previous.length && result.every((part, index) =>
    part === previous[index]) ? previous : result;
}

function isToolItem(item: ThreadItem): boolean {
  return item.type !== "userMessage" && item.type !== "agentMessage" && item.type !== "plan" &&
    item.type !== "reasoning" && item.type !== "hookPrompt";
}

function completeMissingTool(item: ThreadItem): ThreadItem {
  if (!isToolItem(item) || !("status" in item) ||
    (item.status !== "inProgress" && item.status !== "generating")) return item;
  return { ...item, status: "completed" };
}

function sameItemSnapshot(left: ThreadItem, right: ThreadItem): boolean {
  return left === right || JSON.stringify(left) === JSON.stringify(right);
}

function sameTurnSnapshot(left: Turns[number], right: Turns[number]): boolean {
  if (left === right) return true;
  return turnSnapshotKey(left) === turnSnapshotKey(right);
}

function turnSnapshotKey(turn: Turns[number]): string {
  const cached = turnSnapshotKeys.get(turn);
  if (cached) return cached;
  const value = JSON.stringify(turn);
  turnSnapshotKeys.set(turn, value);
  return value;
}

function sameTurnSequence(left: Turns, right: Turns): boolean {
  return left.length === right.length && left.every((turn, index) => turn === right[index]);
}
