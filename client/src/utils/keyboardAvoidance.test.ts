import { describe, expect, it } from "vitest";

import { keyboardAvoidance } from "./keyboardAvoidance";

describe("keyboardAvoidance", () => {
  it("Android 键盘显示时只增加底部 padding", () => {
    expect(keyboardAvoidance("android", 24, true)).toEqual({
      behavior: "padding",
      keyboardVerticalOffset: 80,
      enabled: true,
    });
  });

  it("Android 键盘收起时清除 padding", () => {
    expect(keyboardAvoidance("android", 24, false)).toEqual({
      behavior: "padding",
      keyboardVerticalOffset: 80,
      enabled: false,
    });
  });

  it("iOS 保留导航栏偏移的 padding 避让", () => {
    expect(keyboardAvoidance("ios", 47)).toEqual({
      behavior: "padding",
      keyboardVerticalOffset: 91,
    });
  });
});
