import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { createSSHSaveDiagnostics, sshSaveProgressLabel, type SSHSaveProgress } from "./sshSaveDiagnostics";

describe("SSH 保存阶段诊断", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.spyOn(console, "info").mockImplementation(() => undefined);
    vi.spyOn(console, "error").mockImplementation(() => undefined);
  });

  afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks(); });

  it("标记实际超时阶段但允许原操作随后完成", () => {
    const events: SSHSaveProgress[] = [];
    const diagnostics = createSSHSaveDiagnostics((event) => events.push(event));
    diagnostics.start("store-private-key");
    vi.advanceTimersByTime(30_000);
    expect(events.at(-1)).toEqual({ stage: "store-private-key", status: "timeout", elapsedMs: 30_000 });
    diagnostics.start("write-profile");
    diagnostics.complete();
    expect(events.map(({ stage, status }) => [stage, status])).toEqual([
      ["store-private-key", "running"], ["store-private-key", "timeout"],
      ["store-private-key", "completed"], ["write-profile", "running"], ["write-profile", "completed"],
    ]);
    vi.advanceTimersByTime(60_000);
    expect(events).toHaveLength(5);
  });

  it.each(["complete", "cancel", "fail"] as const)("%s 清理阶段计时器", (method) => {
    const onProgress = vi.fn();
    const diagnostics = createSSHSaveDiagnostics(onProgress);
    diagnostics.start("inspect-runtime");
    diagnostics[method](new Error("不应记录的原始异常"));
    const count = onProgress.mock.calls.length;
    vi.advanceTimersByTime(60_000);
    expect(onProgress).toHaveBeenCalledTimes(count);
    expect(vi.getTimerCount()).toBe(0);
  });

  it("等待人工确认不触发操作超时", () => {
    const onProgress = vi.fn();
    const diagnostics = createSSHSaveDiagnostics(onProgress);
    diagnostics.start("confirm-host");
    vi.advanceTimersByTime(600_000);
    expect(onProgress).toHaveBeenCalledTimes(1);
    diagnostics.cancel();
    expect(onProgress.mock.calls.at(-1)?.[0].status).toBe("cancelled");
  });

  it.each([
    ["E_SECURESTORE_WRITE_ERROR", "E_SECURESTORE_WRITE_ERROR"],
    ["SQLITE_BUSY", "SQLITE_BUSY"], [-34018, "-34018"],
    ["https://private.example/secret", "Error"],
    ["E_secret_password", "Error"], [Number.NaN, "Error"],
  ])("错误码 %s 仅保留允许的格式", (code, expected) => {
    const onProgress = vi.fn();
    const diagnostics = createSSHSaveDiagnostics(onProgress);
    diagnostics.start("store-private-key");
    diagnostics.fail(Object.assign(new Error("秘密内容"), { code }));
    expect(onProgress.mock.calls.at(-1)?.[0].errorCode).toBe(expected);
  });

  it("日志和界面状态不包含异常消息及连接凭据", () => {
    const events: SSHSaveProgress[] = [];
    const diagnostics = createSSHSaveDiagnostics((event) => events.push(event));
    const secret = "private-key password pairing-uri https://private.example /private/path";
    const error = Object.assign(new Error(secret), { name: secret, code: secret });
    diagnostics.start("inspect-runtime");
    diagnostics.fail(error);
    expect(events.at(-1)?.errorCode).toBe("UNKNOWN");
    const output = JSON.stringify({ events, info: vi.mocked(console.info).mock.calls,
      errors: vi.mocked(console.error).mock.calls, labels: events.map(sshSaveProgressLabel) });
    for (const value of secret.split(" ")) expect(output).not.toContain(value);
  });

  it("中文状态明确指出阶段及结果", () => {
    expect(sshSaveProgressLabel({ stage: "write-profile", status: "timeout", elapsedMs: 30_900 }))
      .toBe("写入连接信息仍未返回（已等待 30 秒）");
    expect(sshSaveProgressLabel({ stage: "reload-profiles", status: "failed", elapsedMs: 0, errorCode: "SQLITE_BUSY" }))
      .toBe("刷新连接列表失败（SQLITE_BUSY）");
  });
});
