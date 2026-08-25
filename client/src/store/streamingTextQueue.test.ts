import { describe, expect, it } from "vitest";

import { STREAM_CHARACTERS_PER_FRAME, StreamingTextQueue,
  type StreamingDelta } from "./streamingTextQueue";

describe("官方文本逐帧揭示队列", () => {
  it("同一目标每帧最多揭示 24 个 Unicode 字符", () => {
    const applied: StreamingDelta[] = [];
    const frames: (() => void)[] = [];
    const queue = new StreamingTextQueue((delta) => applied.push(delta),
      (callback) => frames.push(callback));
    queue.enqueue(delta("item", "你".repeat(STREAM_CHARACTERS_PER_FRAME + 3)));

    frames.shift()?.();
    expect(Array.from(applied[0]?.delta ?? "")).toHaveLength(STREAM_CHARACTERS_PER_FRAME);
    frames.shift()?.();
    expect(Array.from(applied[1]?.delta ?? "")).toHaveLength(3);
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

  it("完成事件会等待 1000 字符全部排空", async () => {
    const applied: StreamingDelta[] = [];
    const frames: (() => void)[] = [];
    const queue = new StreamingTextQueue((value) => applied.push(value),
      (callback) => frames.push(callback));
    queue.enqueue(delta("long", "x".repeat(1_000)));
    const flushed = queue.flushTurn("thread", "turn");

    while (frames.length > 0) frames.shift()?.();
    await flushed;

    expect(applied.map((item) => item.delta).join("")).toHaveLength(1_000);
  });
});

function delta(itemId = "item", value = "text"): StreamingDelta {
  return { threadId: "thread", turnId: "turn", itemId, target: "agent", index: 0,
    delta: value };
}
