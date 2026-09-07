import type { ASTNode } from "react-native-markdown-display";

export const MARKDOWN_TABLE_VISIBLE_COLUMNS = 3;

export function markdownTableColumnCount(node: Pick<ASTNode, "type" | "children">): number {
  if (node.type === "tr") {
    return node.children.filter((child) => child.type === "th" || child.type === "td").length;
  }
  return node.children.reduce((maximum, child) =>
    Math.max(maximum, markdownTableColumnCount(child)), 0);
}

export function markdownTableMetrics(parentWidth: number, columnCount: number): {
  columnWidth: number;
  tableWidth: number;
} {
  if (parentWidth <= 0 || columnCount <= 0) return { columnWidth: 0, tableWidth: 0 };
  const columnWidth = parentWidth / MARKDOWN_TABLE_VISIBLE_COLUMNS;
  return { columnWidth, tableWidth: columnWidth * columnCount };
}
