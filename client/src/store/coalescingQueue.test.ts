import { describe, expect, it } from "vitest";

import { CoalescingKeyedQueue } from "./coalescingQueue";

describe("尾部失效信号合并", () => {
  it("同一 Thread 串行刷新，并把执行期间的重复信号合并成一次追赶", async () => {
    const queue = new CoalescingKeyedQueue();
    let release!: () => void;
    const gate = new Promise<void>((resolve) => { release = resolve; });
    let runs = 0;
    const operation = async () => { if (++runs === 1) await gate; };

    const first = queue.run("profile:thread", operation);
    const second = queue.run("profile:thread", operation);
    const third = queue.run("profile:thread", operation);
    expect(first).toBe(second);
    expect(second).toBe(third);
    release();
    await first;

    expect(runs).toBe(2);
  });

  it("不同 Thread 可独立刷新", async () => {
    const queue = new CoalescingKeyedQueue();
    const calls: string[] = [];
    await Promise.all([
      queue.run("thread-a", async () => { calls.push("a"); }),
      queue.run("thread-b", async () => { calls.push("b"); }),
    ]);
    expect(calls.sort()).toEqual(["a", "b"]);
  });
});
