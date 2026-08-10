import type { ServerRequest } from "@codex-app-server/ServerRequest";
import type { Turn } from "@codex-app-server/v2/Turn";

export type ConversationRow =
  | { kind: "turn"; key: string; turn: Turn }
  | { kind: "request"; key: string; request: ServerRequest };

const turnRows = new WeakMap<Turn, Extract<ConversationRow, { kind: "turn" }>>();
const requestRows = new WeakMap<object, Extract<ConversationRow, { kind: "request" }>>();

/**
 * 尾部流式刷新只为实际变化的 Turn 创建新 Row，避免 FlashList 重绑全部可见 Cell。
 */
export function conversationRows(turns: Turn[], requests: ServerRequest[]): ConversationRow[] {
  return [...turns.map(turnRow), ...requests.map(requestRow)];
}

function turnRow(turn: Turn): Extract<ConversationRow, { kind: "turn" }> {
  const cached = turnRows.get(turn);
  if (cached) return cached;
  const row = { kind: "turn" as const, key: `turn:${turn.id}`, turn };
  turnRows.set(turn, row);
  return row;
}

function requestRow(request: ServerRequest): Extract<ConversationRow, { kind: "request" }> {
  const cached = requestRows.get(request);
  if (cached) return cached;
  const row = { kind: "request" as const, key: `request:${String(request.id)}`, request };
  requestRows.set(request, row);
  return row;
}
