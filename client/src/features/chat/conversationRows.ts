import type { ServerRequest } from "@codex-app-server/ServerRequest";
import type { ASTNode } from "react-native-markdown-display";
import type { MobileTurn } from "@/app-server/types";
import type { MarkdownSplitter } from "./markdownBlocks";
import { projectTurnPresentation, streamingTextItemId, toolOperationLines,
  type ToolOperation, type TurnBlock, type ToolItem } from "./turnPresentation";
import { turnExpanded, turnWithDetails, type TurnDetailState } from "./turnDetails";
import { visibleTurnAnswers } from "./turnAnswers";

type RowIdentity = { key: string; turnId: string };
export type ConversationRow =
  | RowIdentity & { kind: "block"; block: TurnBlock; live: boolean; ast?: ASTNode[] | undefined;
    sizeHint?: number | undefined; resolveAst?: (() => ASTNode[]) | undefined }
  | RowIdentity & { kind: "activity"; turn: MobileTurn; expanded: boolean; noFinal: boolean }
  | RowIdentity & { kind: "detailPage"; detail: TurnDetailState | undefined }
  | RowIdentity & { kind: "operation"; item: ToolItem; operation: ToolOperation }
  | RowIdentity & { kind: "status"; turn: MobileTurn; thinking: boolean }
  | { kind: "request"; key: string; request: ServerRequest };

export type RowOptions = {
  details?: ReadonlyMap<string, TurnDetailState>;
  expandedTools?: ReadonlySet<string>;
  paginated?: boolean;
  splitMarkdown?: MarkdownSplitter;
};
type CachedRows = { detail: TurnDetailState | undefined;
  tools: RowOptions["expandedTools"]; paginated: boolean | undefined;
  split: MarkdownSplitter | undefined; rows: ConversationRow[] };
const requestRows = new WeakMap<object, Extract<ConversationRow, { kind: "request" }>>();

/** 流式 Turn 会重建行描述，但未变化的正文与 AST 不应重复提交到原生视图。 */
export function sameConversationRow(left: ConversationRow, right: ConversationRow): boolean {
  if (left === right) return true;
  if (left.key !== right.key || left.kind !== "block" || right.kind !== "block" ||
    left.live !== right.live || left.ast !== right.ast || left.resolveAst !== right.resolveAst) return false;
  const a = left.block, b = right.block;
  if (a.kind !== b.kind) return false;
  if ("item" in a && "item" in b) return a.item === b.item;
  if (a.kind === "tools" && b.kind === "tools") return a.title === b.title &&
    a.running === b.running && a.inferredRunning === b.inferredRunning &&
    a.category === b.category && a.items.length === b.items.length &&
    a.items.every((item, index) => item === b.items[index]);
  return a.kind === "generatedImage" && b.kind === "generatedImage" &&
    a.image.id === b.image.id && a.image.source === b.image.source;
}

export function createConversationRowProjector() {
  // 详情行持有已加载 Item；缓存必须和页面同寿命，避免全局缓存使离页详情无法释放。
  const cache = new WeakMap<MobileTurn, CachedRows>();
  return (turns: MobileTurn[], requests: ServerRequest[], options: RowOptions = {}): ConversationRow[] =>
    [...turns.flatMap((turn) => rowsForTurn(turn, options, cache)), ...requests.map(requestRow)];
}
export const conversationRows = createConversationRowProjector();

function rowsForTurn(turn: MobileTurn, options: RowOptions,
  turnRows: WeakMap<MobileTurn, CachedRows>): ConversationRow[] {
  const detail = options.details?.get(turn.id);
  const cached = turnRows.get(turn);
  if (cached && cached.detail === detail && cached.tools === options.expandedTools &&
    cached.split === options.splitMarkdown && cached.paginated === options.paginated) return cached.rows;
  const rows: ConversationRow[] = [];
  const expanded = turnExpanded(turn, detail);
  // 收起时只寻找两端内容，不投影工具、推理、图片或中间 Markdown。
  const user = turn.items.find((item) => item.type === "userMessage");
  const finals = visibleTurnAnswers(turn);
  const liveId = streamingTextItemId(turn);
  const pushBlock = (block: TurnBlock) => {
    const key = `turn:${turn.id}:item:${block.key}`;
    const live = block.key === liveId;
    if (options.splitMarkdown && !live && (block.kind === "final" ||
      block.kind === "commentary" || block.kind === "plan")) {
      for (const part of options.splitMarkdown(block.item.text, key)) {
        rows.push({ kind: "block", key: `${key}:markdown:${part.key}`, turnId: turn.id,
          block, live, ast: part.resolveAst ? undefined : part.ast,
          sizeHint: part.sizeHint, resolveAst: part.resolveAst });
      }
    } else rows.push({ kind: "block", key, turnId: turn.id, block, live });
    if (block.kind === "tools" && options.expandedTools?.has(key)) {
      for (const item of block.items) {
        for (const operation of toolOperationLines(item, block.inferredRunning)) {
          rows.push({ kind: "operation", key: `${key}:operation:${operation.key}`,
            turnId: turn.id, item, operation });
        }
      }
    }
  };
  if (user) pushBlock({ kind: "user", key: user.id, item: user });
  rows.push({ kind: "activity", key: `turn:${turn.id}:activity`, turnId: turn.id,
    turn, expanded, noFinal: turn.status === "completed" && finals.length === 0 });
  if (expanded) {
    const needsPage = options.paginated && (!detail?.loaded || detail.nextCursor !== null || detail.loading);
    if (needsPage && detail?.direction === "desc") rows.push({ kind: "detailPage",
      key: `turn:${turn.id}:page`, turnId: turn.id, detail });
    const source = options.paginated && !detail
      ? { ...turn, items: turn.items.filter((item) => item.type === "userMessage" ||
        finals.some((answer) => answer.id === item.id)) }
      : options.paginated ? turnWithDetails(turn, detail) : turn;
    const projected = projectTurnPresentation(source);
    for (const block of projected.blocks) {
      if (block.kind === "user" && block.item.id === user?.id) continue;
      if ((block.kind === "final" || block.kind === "commentary") &&
        finals.some((item) => item.id === block.item.id)) continue;
      pushBlock(block);
    }
    if (needsPage && detail?.direction !== "desc") rows.push({ kind: "detailPage",
      key: `turn:${turn.id}:page`, turnId: turn.id, detail });
  }
  for (const item of finals) {
    if (item.type === "agentMessage") pushBlock({ kind: "final", key: item.id, item });
  }
  if (turn.status === "failed" || turn.status === "interrupted" || turn.status === "inProgress") {
    rows.push({ kind: "status", key: `turn:${turn.id}:status`, turnId: turn.id, turn,
      thinking: turn.status === "inProgress" && !liveId });
  }
  turnRows.set(turn, { detail, tools: options.expandedTools, split: options.splitMarkdown,
    paginated: options.paginated, rows });
  return rows;
}

function requestRow(request: ServerRequest): Extract<ConversationRow, { kind: "request" }> {
  const cached = requestRows.get(request);
  if (cached) return cached;
  const row = { kind: "request" as const, key: `request:${String(request.id)}`, request };
  requestRows.set(request, row);
  return row;
}
