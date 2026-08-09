import { describe, expect, it } from "vitest";

import { ACTIVITY_TOGGLE_SCROLL_SETTLE_MS, activityToggleAllowed,
  INITIAL_ACTIVITY_RENDER_COUNT, isTurnActivityCollapsed, nextActivityRenderCount,
  toggleToolDisclosure, toggleTurnActivity, toolDisclosureExpanded } from "./activityDisclosure";

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

  it("列表拖动和停止后的短暂结算期不允许误触折叠头", () => {
    expect(activityToggleAllowed(Number.POSITIVE_INFINITY, 1000)).toBe(false);
    expect(activityToggleAllowed(1000 + ACTIVITY_TOGGLE_SCROLL_SETTLE_MS,
      1000 + ACTIVITY_TOGGLE_SCROLL_SETTLE_MS - 1)).toBe(false);
    expect(activityToggleAllowed(1000 + ACTIVITY_TOGGLE_SCROLL_SETTLE_MS,
      1000 + ACTIVITY_TOGGLE_SCROLL_SETTLE_MS)).toBe(true);
  });

  it("长过程按小批次推进并严格停在活动总数", () => {
    expect(nextActivityRenderCount(0, 100)).toBe(INITIAL_ACTIVITY_RENDER_COUNT);
    expect(nextActivityRenderCount(INITIAL_ACTIVITY_RENDER_COUNT, 100))
      .toBe(INITIAL_ACTIVITY_RENDER_COUNT + 4);
    expect(nextActivityRenderCount(98, 100)).toBe(100);
    expect(nextActivityRenderCount(0, 0)).toBe(0);
  });
});
