import { describe, expect, it } from "vitest";

import { buildInteractiveAnswer } from "./interactiveAnswer";

describe("buildInteractiveAnswer", () => {
  it("按 Codex requestUserInput 协议包装每个问题的答案", () => {
    expect(buildInteractiveAnswer({ choice: "继续", note: "补充说明" })).toEqual({
      answers: {
        choice: { answers: ["继续"] },
        note: { answers: ["补充说明"] },
      },
    });
  });
});
