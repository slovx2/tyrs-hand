import { describe, expect, it } from "vitest";

import { StreamingTextQueue, type StreamingDelta } from "./streamingTextQueue";

describe("官方文本逐帧合并队列", () => {
  it("同一目标在一帧内只提交一次合并更新", () => {
    const applied: StreamingDelta[] = [];
    const frames: (() => void)[] = [];
    const queue = new StreamingTextQueue((delta) => applied.push(delta),
      (callback) => frames.push(callback));
    queue.enqueue(delta("item", "第一段"));
    queue.enqueue(delta("item", "第二段"));

    frames.shift()?.();
    expect(applied.map((item) => item.delta)).toEqual(["第一段第二段"]);
    expect(frames).toHaveLength(0);
  });

  it("完成前排空目标缓冲，其他 Item 继续独立流式更新", async () => {
    const applied: StreamingDelta[] = [];
    const frames: (() => void)[] = [];
    const queue = new StreamingTextQueue((value) => applied.push(value),
      (callback) => frames.push(callback));
    queue.enqueue(delta("first", "a".repeat(30)));
    queue.enqueue(delta("second", "b".repeat(30)));
    const flushed = queue.flushItem("thread", "turn", "first");

    while (frames.length > 0) frames.shift()?.();
    await flushed;

    expect(applied.filter((item) => item.itemId === "first").map((item) => item.delta).join(""))
      .toBe("a".repeat(30));
    expect(applied.filter((item) => item.itemId === "second").map((item) => item.delta).join(""))
      .toBe("b".repeat(30));
  });

  it("完成事件会等待同一帧内的 1000 字符全部排空", async () => {
    const applied: StreamingDelta[] = [];
    const frames: (() => void)[] = [];
    const queue = new StreamingTextQueue((value) => applied.push(value),
      (callback) => frames.push(callback));
    queue.enqueue(delta("long", "x".repeat(1_000)));
    const flushed = queue.flushTurn("thread", "turn");

    while (frames.length > 0) frames.shift()?.();
    await flushed;

    expect(applied).toHaveLength(1);
    expect(applied.map((item) => item.delta).join("")).toHaveLength(1_000);
  });
});

function delta(itemId = "item", value = "text"): StreamingDelta {
  return { threadId: "thread", turnId: "turn", itemId, target: "agent", index: 0,
    delta: value };
}
