export type ConversationPosition =
  | { kind: "latest" }
  | { kind: "anchor"; rowKey: string; topOffset: number };

export type ResolvedConversationPosition =
  | { kind: "latest" }
  | { kind: "anchor"; index: number; topOffset: number };

const positions = new Map<string, ConversationPosition>();

export function loadConversationPosition(key: string): ConversationPosition | null {
  return positions.get(key) ?? null;
}

export function saveConversationPosition(key: string, position: ConversationPosition): void {
  positions.set(key, position.kind === "anchor"
    ? { ...position, topOffset: Number.isFinite(position.topOffset) ? position.topOffset : 0 }
    : position);
}

export function clearConversationPositions(): void {
  positions.clear();
}

export function resolveConversationPosition(position: ConversationPosition | null,
  rowKeys: string[]): ResolvedConversationPosition {
  if (position?.kind !== "anchor") return { kind: "latest" };
  const index = rowKeys.indexOf(position.rowKey);
  return index < 0 ? { kind: "latest" }
    : { kind: "anchor", index, topOffset: position.topOffset };
}

export function visibleRowTop(layoutY: number, firstItemOffset: number,
  scrollOffset: number): number {
  return layoutY + firstItemOffset - scrollOffset;
}

export function anchorViewOffset(topOffset: number): number {
  return -topOffset;
}

export function conversationScrollState(contentHeight: number, viewportHeight: number,
  offsetY: number): { distanceFromBottom: number; pinnedToLatest: boolean; showLatest: boolean } {
  const distanceFromBottom = Math.max(0, contentHeight - viewportHeight - offsetY);
  return {
    distanceFromBottom,
    pinnedToLatest: distanceFromBottom <= 64,
    showLatest: distanceFromBottom > 160,
  };
}
