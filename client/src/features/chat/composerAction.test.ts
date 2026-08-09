import { describe, expect, it } from "vitest";

import { composerPrimaryAction } from "./composerAction";

describe("会话输入框主操作", () => {
  it("空输入随 Turn 状态在发送与停止之间自动切换", () => {
    expect(composerPrimaryAction(false, false, false))
      .toEqual({ kind: "send", disabled: true });
    expect(composerPrimaryAction(true, false, false))
      .toEqual({ kind: "stop", disabled: false });
  });

  it("活动 Turn 输入 steer 内容后恢复为发送按钮", () => {
    expect(composerPrimaryAction(true, true, false))
      .toEqual({ kind: "send", disabled: false });
  });

  it("提交或停止期间保持当前图标但禁止重复操作", () => {
    expect(composerPrimaryAction(true, false, true))
      .toEqual({ kind: "stop", disabled: true });
    expect(composerPrimaryAction(false, true, true))
      .toEqual({ kind: "send", disabled: true });
  });
});
