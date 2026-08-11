import { describe, expect, it } from "vitest";

import { ACTIVITY_TOGGLE_SCROLL_SETTLE_MS, activityToggleAllowed,
  isTurnActivityCollapsed, toggleToolDisclosure, toggleTurnActivity,
  toolDisclosureExpanded } from "./activityDisclosure";

describe("官方活动披露状态", () => {
  it("工具组默认收起，运行和完成状态都不会自动改变披露状态", () => {
    expect(toolDisclosureExpanded(undefined)).toBe(false);
    expect(toolDisclosureExpanded(false)).toBe(false);
    expect(toolDisclosureExpanded(false)).toBe(false);
  });

  it("只由用户切换工具组，后续状态刷新继续保持选择", () => {
    const expanded = toggleToolDisclosure(undefined);
    expect(toolDisclosureExpanded(expanded)).toBe(true);
    expect(toolDisclosureExpanded(expanded)).toBe(true);
    expect(toolDisclosureExpanded(toggleToolDisclosure(expanded))).toBe(false);
  });

  it("Turn 手动展开状态在尾部刷新和列表回收后仍按稳定 key 保持", () => {
    const key = "profile:thread:turn-manual";
    expect(isTurnActivityCollapsed(key, true)).toBe(true);
    expect(toggleTurnActivity(key, true)).toBe(true);
    expect(isTurnActivityCollapsed(key, true)).toBe(false);
    expect(isTurnActivityCollapsed(key, false)).toBe(false);
    expect(isTurnActivityCollapsed(key, true)).toBe(false);
  });

  it("列表拖动和停止后的短暂结算期不允许误触折叠头", () => {
    expect(activityToggleAllowed(Number.POSITIVE_INFINITY, 1000)).toBe(false);
    expect(activityToggleAllowed(1000 + ACTIVITY_TOGGLE_SCROLL_SETTLE_MS,
      1000 + ACTIVITY_TOGGLE_SCROLL_SETTLE_MS - 1)).toBe(false);
    expect(activityToggleAllowed(1000 + ACTIVITY_TOGGLE_SCROLL_SETTLE_MS,
      1000 + ACTIVITY_TOGGLE_SCROLL_SETTLE_MS)).toBe(true);
  });
});
