import type { Thread } from "@codex-app-server/v2/Thread";

import type { OfficialTurnPage } from "@/app-server/officialClient";

type Turns = Thread["turns"];

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
  return overlap < 0 ? { turns: incoming, overlapped: false }
    : { turns: mergeTurnSequence(existing.slice(0, overlap), incoming), overlapped: true };
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
    turns.set(turn.id, turn);
  }
  return order.flatMap((id) => turns.get(id) ?? []);
}
