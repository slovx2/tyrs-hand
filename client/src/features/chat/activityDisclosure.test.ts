import { describe, expect, it } from "vitest";

import { isTurnActivityCollapsed, toggleToolDisclosure, toggleTurnActivity,
  toolDisclosureExpanded } from "./activityDisclosure";

describe("官方活动披露状态", () => {
  it("工具运行时默认展开，结束后使用独立状态自动收起", () => {
    const initial = undefined;
    expect(toolDisclosureExpanded(initial, true)).toBe(true);

    const collapsedWhileRunning = toggleToolDisclosure(initial, true);
    expect(toolDisclosureExpanded(collapsedWhileRunning, true)).toBe(false);
    expect(toolDisclosureExpanded(collapsedWhileRunning, false)).toBe(false);
  });

  it("完成后用户可以再次展开，且不改变运行时状态", () => {
    const completedExpanded = toggleToolDisclosure(undefined, false);
    expect(toolDisclosureExpanded(completedExpanded, false)).toBe(true);
    expect(toolDisclosureExpanded(completedExpanded, true)).toBe(true);
  });

  it("Turn 手动展开状态在尾部刷新和列表回收后仍按稳定 key 保持", () => {
    const key = "profile:thread:turn-manual";
    expect(isTurnActivityCollapsed(key, true)).toBe(true);
    expect(toggleTurnActivity(key, true)).toBe(true);
    expect(isTurnActivityCollapsed(key, true)).toBe(false);
    expect(isTurnActivityCollapsed(key, false)).toBe(false);
    expect(isTurnActivityCollapsed(key, true)).toBe(false);
  });
});
