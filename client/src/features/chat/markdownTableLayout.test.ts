import { describe, expect, it } from "vitest";

import { markdownTableColumnCount, markdownTableMetrics } from "./markdownTableLayout";

type TableNode = Parameters<typeof markdownTableColumnCount>[0];

function node(type: string, children: TableNode[] = []): TableNode {
  return { type, children } as TableNode;
}

describe("markdownTableColumnCount", () => {
  it("使用行中的最大列数", () => {
    const table = node("table", [
      node("thead", [node("tr", [node("th"), node("th"), node("th")])]),
      node("tbody", [node("tr", [node("td"), node("td"), node("td"), node("td")])]),
    ]);

    expect(markdownTableColumnCount(table)).toBe(4);
  });
});

describe("markdownTableMetrics", () => {
  it("每列固定为父容器宽度的三分之一", () => {
    expect(markdownTableMetrics(360, 2)).toEqual({ columnWidth: 120, tableWidth: 240 });
    expect(markdownTableMetrics(360, 6)).toEqual({ columnWidth: 120, tableWidth: 720 });
  });

  it("布局尚未测量时不生成无效尺寸", () => {
    expect(markdownTableMetrics(0, 6)).toEqual({ columnWidth: 0, tableWidth: 0 });
  });
});
