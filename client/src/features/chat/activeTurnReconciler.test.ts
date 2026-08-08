import { afterEach, describe, expect, it, vi } from "vitest";

import { createActiveTurnReconciler } from "./activeTurnReconciler";

describe("活动 Turn 权威对账", () => {
  afterEach(() => vi.useRealTimers());

  it("立即执行并等待上一轮完成后才安排下一轮", async () => {
    vi.useFakeTimers();
    let finishFirst!: () => void;
    const first = new Promise<void>((resolve) => { finishFirst = resolve; });
    const reconcile = vi.fn()
      .mockImplementationOnce(() => first)
      .mockResolvedValue(undefined);
    const loop = createActiveTurnReconciler(reconcile, 1_800);

    loop.start();
    expect(reconcile).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(5_000);
    expect(reconcile).toHaveBeenCalledTimes(1);

    finishFirst();
    await Promise.resolve();
    await vi.advanceTimersByTimeAsync(1_799);
    expect(reconcile).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(reconcile).toHaveBeenCalledTimes(2);
    loop.dispose();
  });

  it("进入后台停止，回到前台时立即重新对账", async () => {
    vi.useFakeTimers();
    const reconcile = vi.fn().mockResolvedValue(undefined);
    const loop = createActiveTurnReconciler(reconcile, 1_800);

    loop.start();
    await Promise.resolve();
    loop.stop();
    await vi.advanceTimersByTimeAsync(3_600);
    expect(reconcile).toHaveBeenCalledTimes(1);

    loop.start();
    expect(reconcile).toHaveBeenCalledTimes(2);
    loop.dispose();
  });
});
