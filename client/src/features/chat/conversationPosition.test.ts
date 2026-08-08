import { beforeEach, describe, expect, it } from "vitest";

import { anchorViewOffset, clearConversationPositions, conversationScrollState,
  loadConversationPosition, resolveConversationPosition, saveConversationPosition,
  visibleRowTop } from "./conversationPosition";

describe("会话阅读位置", () => {
  beforeEach(() => clearConversationPositions());

  it("按 profile + thread 隔离，并只在本次运行内保存稳定 Row 锚点", () => {
    saveConversationPosition("profile-a:thread-1",
      { kind: "anchor", rowKey: "turn-2:item-1", topOffset: 18 });
    saveConversationPosition("profile-b:thread-1", { kind: "latest" });

    expect(loadConversationPosition("profile-a:thread-1")).toEqual(
      { kind: "anchor", rowKey: "turn-2:item-1", topOffset: 18 });
    expect(loadConversationPosition("profile-b:thread-1")).toEqual({ kind: "latest" });
  });

  it("锚点存在时恢复，锚点失效时回退最新", () => {
    const saved = { kind: "anchor" as const, rowKey: "turn-2:item-1", topOffset: 12 };
    expect(resolveConversationPosition(saved, ["turn-1:item-1", "turn-2:item-1"]))
      .toEqual({ kind: "anchor", index: 1, topOffset: 12 });
    expect(resolveConversationPosition(saved, ["turn-3:item-1"]))
      .toEqual({ kind: "latest" });
  });

  it("旧页前插或 Markdown 增高时保持 Row 相对视口偏移", () => {
    const before = visibleRowTop(600, 8, 560);
    const afterPrepend = visibleRowTop(1100, 8, 1060);
    const afterMarkdownLayout = visibleRowTop(1220, 8, 1180);

    expect(before).toBe(48);
    expect(afterPrepend).toBe(before);
    expect(afterMarkdownLayout).toBe(before);
    expect(anchorViewOffset(before)).toBe(-48);
  });

  it("64px 内跟随最新，超过 160px 才显示回到底部按钮", () => {
    expect(conversationScrollState(1000, 500, 436)).toMatchObject({
      distanceFromBottom: 64, pinnedToLatest: true, showLatest: false,
    });
    expect(conversationScrollState(1000, 500, 435)).toMatchObject({
      distanceFromBottom: 65, pinnedToLatest: false, showLatest: false,
    });
    expect(conversationScrollState(1000, 500, 339)).toMatchObject({
      distanceFromBottom: 161, pinnedToLatest: false, showLatest: true,
    });
  });

  it("键盘改变视口后，只要列表同步保持底部就继续跟随", () => {
    expect(conversationScrollState(1000, 600, 400).pinnedToLatest).toBe(true);
    expect(conversationScrollState(1000, 360, 640).pinnedToLatest).toBe(true);
  });
});
