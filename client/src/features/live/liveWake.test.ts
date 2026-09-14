import { describe, expect, it } from "vitest";

import { isLiveWakeParam, shouldConsumeLiveWake, shouldPlaySessionStartedSound } from "./liveWake";

describe("Live 语音唤醒", () => {
  it("只识别 wake=1", () => {
    expect(isLiveWakeParam("1")).toBe(true);
    expect(isLiveWakeParam(["1"])).toBe(true);
    expect(isLiveWakeParam("0")).toBe(false);
    expect(isLiveWakeParam(undefined)).toBe(false);
  });

  it("同一个唤醒参数只消费一次", () => {
    expect(shouldConsumeLiveWake("1", false)).toBe(true);
    expect(shouldConsumeLiveWake("1", true)).toBe(false);
  });
});

describe("Live 连接完成提示音", () => {
  it("session.started 只触发一次", () => {
    expect(shouldPlaySessionStartedSound("session.started", false)).toBe(true);
    expect(shouldPlaySessionStartedSound("session.started", true)).toBe(false);
    expect(shouldPlaySessionStartedSound("session.closed", false)).toBe(false);
  });
});
