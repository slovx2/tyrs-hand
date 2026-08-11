import { describe, expect, it } from "vitest";

import { beginUserScroll, updateUserScroll } from "./conversationUserScroll";

describe("会话用户滚动意图", () => {
  it("内容增长但 offset 不变时不退出跟随", () => {
    const gesture = beginUserScroll(500);
    expect(updateUserScroll(gesture, 500, 80).intent).toBe("none");
  });

  it("用户真实向上阅读超过 24px 时只报告一次离开", () => {
    const gesture = beginUserScroll(500);
    const below = updateUserScroll(gesture, 477, 47);
    const away = updateUserScroll(below.gesture, 475, 49);
    const continued = updateUserScroll(away.gesture, 430, 94);

    expect(below.intent).toBe("none");
    expect(away.intent).toBe("away");
    expect(continued.intent).toBe("none");
  });

  it("手动回到底部后恢复，后续内容增长不再次离开", () => {
    const away = updateUserScroll(beginUserScroll(500), 450, 74);
    const bottom = updateUserScroll(away.gesture, 524, 0);
    const growth = updateUserScroll(bottom.gesture, 524, 52);

    expect(away.intent).toBe("away");
    expect(bottom.intent).toBe("bottom");
    expect(growth.intent).toBe("none");
  });

  it("拖动结束后的惯性位移延续同一手势", () => {
    const drag = updateUserScroll(beginUserScroll(600), 584, 40);
    const momentum = updateUserScroll(drag.gesture, 560, 64);

    expect(drag.intent).toBe("none");
    expect(momentum.intent).toBe("away");
  });
});
