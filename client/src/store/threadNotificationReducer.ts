import type { ServerNotification } from "@codex-app-server/ServerNotification";
import type { ThreadItem } from "@codex-app-server/v2/ThreadItem";
import type { Turn } from "@codex-app-server/v2/Turn";

import { projectItemForMobile, projectTurnForMobile } from "@/app-server/mobileProjection";
import type { MobileThreadItem, MobileTurn, ThreadRecord } from "@/app-server/types";
import { mergeItemSnapshot, mergeTurnSnapshot } from "./threadHistory";

export type ThreadNotificationResult = {
  record: ThreadRecord;
  changed: boolean;
  needsRefresh: boolean;
  terminal: boolean;
};

export function reduceThreadNotification(record: ThreadRecord,
  event: ServerNotification): ThreadNotificationResult {
  switch (event.method) {
  case "turn/started":
    return updateTurn(record, projectTurnForMobile(event.params.turn), true, false);
  case "item/started":
    return updateItem(record, event.params.turnId, projectItemForMobile(event.params.item), false);
  case "item/completed":
    return updateItem(record, event.params.turnId, projectItemForMobile(event.params.item), true);
  case "item/agentMessage/delta":
    return updateText(record, event.params.turnId, event.params.itemId, "agentMessage",
      event.params.delta);
  case "item/plan/delta":
    return updateText(record, event.params.turnId, event.params.itemId, "plan", event.params.delta);
  case "item/reasoning/summaryPartAdded":
    return updateReasoningPart(record, event.params.turnId, event.params.itemId,
      "summary", event.params.summaryIndex, "");
  case "item/reasoning/summaryTextDelta":
    return updateReasoningPart(record, event.params.turnId, event.params.itemId,
      "summary", event.params.summaryIndex, event.params.delta);
  case "item/reasoning/textDelta":
    return updateReasoningPart(record, event.params.turnId, event.params.itemId,
      "content", event.params.contentIndex, event.params.delta);
  case "turn/completed":
    return updateTurn(record, projectTurnForMobile(event.params.turn), false, true);
  default:
    return unchanged(record, false);
  }
}

export function isDirectThreadNotification(event: { method: string }): event is ServerNotification {
  return event.method === "turn/started" || event.method === "turn/completed" ||
    event.method === "item/started" || event.method === "item/completed" ||
    event.method === "item/agentMessage/delta" || event.method === "item/plan/delta" ||
    event.method === "item/reasoning/summaryPartAdded" ||
    event.method === "item/reasoning/summaryTextDelta" ||
    event.method === "item/reasoning/textDelta";
}

function updateTurn(record: ThreadRecord, incoming: Turn, active: boolean,
  terminal: boolean): ThreadNotificationResult {
  const incomingUsers = incoming.items.filter((item) => item.type === "userMessage");
  let turns = record.thread.turns.filter((turn) =>
    !isMatchingProvisional(turn, incomingUsers));
  const index = turns.findIndex((turn) => turn.id === incoming.id);
  if (index >= 0) {
    // turn/completed 通知在服务端完成瞬间可能只带最终回答。尾页稍后才是权威历史，
    // 因此通知阶段保留已观察到的 commentary、reasoning 和工具，避免整段过程闪退。
    const merged = mergeTurnSnapshot(turns[index]!, incoming,
      { preserveMissingItems: terminal });
    if (merged !== turns[index]) turns = replaceAt(turns, index, merged);
  } else {
    turns = [...turns, incoming];
  }
  const hasActiveTurn = turns.some((turn) => turn.status === "inProgress");
  const status = active || hasActiveTurn
    ? { type: "active" as const, activeFlags: [] }
    : terminal ? { type: "idle" as const } : record.thread.status;
  const thread = turns === record.thread.turns && status === record.thread.status
    ? record.thread : { ...record.thread, status, turns };
  return result(record, thread, false, terminal);
}

function updateItem(record: ThreadRecord, turnId: string, incoming: ThreadItem,
  completed: boolean): ThreadNotificationResult {
  const turnIndex = record.thread.turns.findIndex((turn) => turn.id === turnId);
  if (turnIndex < 0) return unchanged(record, true);
  const turn = record.thread.turns[turnIndex]!;
  const itemIndex = turn.items.findIndex((item) => item.id === incoming.id);
  const items = itemIndex < 0 ? [...turn.items, incoming] : replaceAt(turn.items, itemIndex,
    mergeItemSnapshot(turn.items[itemIndex]!, incoming, completed));
  if (items === turn.items) return unchanged(record, false);
  const turns = replaceAt(record.thread.turns, turnIndex, { ...turn, items });
  return result(record, { ...record.thread, turns }, false, completed);
}

function updateText(record: ThreadRecord, turnId: string, itemId: string,
  expected: "agentMessage" | "plan", delta: string): ThreadNotificationResult {
  if (!delta) return unchanged(record, false);
  return updateExistingItem(record, turnId, itemId, (item, turn) => {
    if (turn.status !== "inProgress" || item.type !== expected) return item;
    return { ...item, text: item.text + delta };
  });
}

function updateReasoningPart(record: ThreadRecord, turnId: string, itemId: string,
  target: "summary" | "content", index: number, delta: string): ThreadNotificationResult {
  return updateExistingItem(record, turnId, itemId, (item, turn) => {
    if (turn.status !== "inProgress" || item.type !== "reasoning") return item;
    const parts = [...item[target]];
    while (parts.length <= index) parts.push("");
    parts[index] = (parts[index] ?? "") + delta;
    return { ...item, [target]: parts };
  });
}

function updateExistingItem(record: ThreadRecord, turnId: string, itemId: string,
  update: (item: MobileThreadItem, turn: MobileTurn) => MobileThreadItem):
  ThreadNotificationResult {
  const turnIndex = record.thread.turns.findIndex((turn) => turn.id === turnId);
  if (turnIndex < 0) return unchanged(record, true);
  const turn = record.thread.turns[turnIndex]!;
  const itemIndex = turn.items.findIndex((item) => item.id === itemId);
  if (itemIndex < 0) return unchanged(record, true);
  const item = update(turn.items[itemIndex]!, turn);
  if (item === turn.items[itemIndex]) return unchanged(record, false);
  const items = replaceAt(turn.items, itemIndex, item);
  const turns = replaceAt(record.thread.turns, turnIndex, { ...turn, items });
  return result(record, { ...record.thread, turns }, false, false);
}

function isMatchingProvisional(turn: MobileTurn,
  incomingUsers: Extract<ThreadItem, { type: "userMessage" }>[]): boolean {
  return turn.id.startsWith("provisional:") && turn.items.some((item) =>
    item.type === "userMessage" && incomingUsers.some((incoming) => {
      if (item.clientId !== null && incoming.clientId !== null) {
        return item.clientId === incoming.clientId;
      }
      return JSON.stringify(item.content) === JSON.stringify(incoming.content);
    }));
}

function replaceAt<Value>(values: Value[], index: number, value: Value): Value[] {
  if (values[index] === value) return values;
  const next = [...values];
  next[index] = value;
  return next;
}

function unchanged(record: ThreadRecord, needsRefresh: boolean): ThreadNotificationResult {
  return { record, changed: false, needsRefresh, terminal: false };
}

function result(record: ThreadRecord, thread: ThreadRecord["thread"], needsRefresh: boolean,
  terminal: boolean): ThreadNotificationResult {
  return { record: thread === record.thread ? record : { ...record, thread },
    changed: thread !== record.thread, needsRefresh, terminal };
}
