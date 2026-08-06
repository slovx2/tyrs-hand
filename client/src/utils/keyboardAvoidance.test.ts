import { describe, expect, it } from "vitest";

import { keyboardAvoidance } from "./keyboardAvoidance";

describe("keyboardAvoidance", () => {
  it("Android 键盘显示时使用高度避让，确保输入框位于键盘上方", () => {
    expect(keyboardAvoidance("android", 24, true)).toEqual({
      behavior: "height",
      keyboardVerticalOffset: 80,
      enabled: true,
    });
  });

  it("Android 键盘收起时禁用高度避让，忽略 React Native 残留的键盘高度", () => {
    expect(keyboardAvoidance("android", 24, false)).toEqual({
      behavior: "height",
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
