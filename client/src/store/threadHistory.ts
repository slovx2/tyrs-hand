import type { Thread } from "@codex-app-server/v2/Thread";

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
    turns.set(turn.id, previous && sameTurnSnapshot(previous, turn) ? previous : turn);
  }
  return order.flatMap((id) => turns.get(id) ?? []);
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
