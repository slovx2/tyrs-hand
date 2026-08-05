import { describe, expect, it } from "vitest";

import { conversationTurnIdFromPayload } from "./updateRouting";

describe("conversationTurnIdFromPayload", () => {
  it("读取标准 message.created 载荷", () => {
    expect(conversationTurnIdFromPayload({ message: { conversationTurnId: "turn-1" } }))
      .toBe("turn-1");
  });

  it("读取旧的扁平 Desktop 载荷", () => {
    expect(conversationTurnIdFromPayload({ conversationTurnId: "turn-2" })).toBe("turn-2");
  });

  it("缺少轮次时返回 null", () => {
    expect(conversationTurnIdFromPayload({ messageId: "message-1" })).toBeNull();
  });
});
