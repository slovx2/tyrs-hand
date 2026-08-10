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

export function mergeTailPage(existing: Turns, incoming: Turns): MergedTailPage {
  const incomingIds = new Set(incoming.map((turn) => turn.id));
  const overlap = existing.findIndex((turn) => incomingIds.has(turn.id));
  if (overlap < 0) return { turns: incoming, overlapped: false };
  const merged = mergeTurnSequence(existing.slice(0, overlap + 1), incoming);
  return { turns: sameTurnSequence(existing, merged) ? existing : merged, overlapped: true };
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
export function mergeTurnSnapshot(previous: Turns[number], incoming: Turns[number]): Turns[number] {
  if (sameTurnSnapshot(previous, incoming)) return previous;
  const incomingById = new Map(incoming.items.map((item) => [item.id, item]));
  const seen = new Set<string>();
  const items: ThreadItem[] = [];
  for (const item of previous.items) {
    const updated = incomingById.get(item.id);
    if (updated) {
      items.push(mergeItemSnapshot(item, updated, incoming.status !== "inProgress"));
      seen.add(item.id);
    } else if (incoming.status === "inProgress" || isToolItem(item)) {
      items.push(item);
      seen.add(item.id);
    }
  }
  for (const item of incoming.items) {
    if (!seen.has(item.id)) items.push(item);
  }
  const merged = { ...incoming, items };
  return sameTurnSnapshot(previous, merged) ? previous : merged;
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
