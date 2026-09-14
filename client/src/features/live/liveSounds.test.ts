import { describe, expect, it } from "vitest";

import { isPlaybackFinishedStatus, shouldPlayLiveConnectionSound, shouldUseNativeLiveCue } from "./liveSoundPolicy";

describe("Live 连接提示音", () => {
  it("关闭时不播放", () => {
    expect(shouldPlayLiveConnectionSound(false)).toBe(false);
    expect(shouldPlayLiveConnectionSound(true)).toBe(true);
  });

  it("Android 走原生通话流提示音", () => {
    expect(shouldUseNativeLiveCue("android")).toBe(true);
    expect(shouldUseNativeLiveCue("ios")).toBe(false);
  });

  it("未加载完成的状态不算播放结束", () => {
    expect(isPlaybackFinishedStatus({ isLoaded: false })).toBe(false);
    expect(isPlaybackFinishedStatus({ isLoaded: true, didJustFinish: false })).toBe(false);
    expect(isPlaybackFinishedStatus({ isLoaded: true, didJustFinish: true })).toBe(true);
  });
});
