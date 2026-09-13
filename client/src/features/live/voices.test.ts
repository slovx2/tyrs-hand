import { describe, expect, it } from "vitest";

import { defaultLiveVoice, findLiveVoice, liveVoices } from "./voices";

describe("Live voices", () => {
  it("提供当前 Live v1 的九个音色并默认使用 Cove", () => {
    expect(liveVoices.map((voice) => voice.slug)).toEqual([
      "arbor", "breeze", "cove", "ember", "juniper",
      "maple", "sol", "spruce", "vale",
    ]);
    expect(defaultLiveVoice).toBe("cove");
    expect(liveVoices.every((voice) => voice.preview)).toBe(true);
  });

  it("未知音色回退到默认音色", () => {
    expect(findLiveVoice("unknown").slug).toBe(defaultLiveVoice);
  });
});
