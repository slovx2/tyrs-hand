export type StreamingDelta = {
  threadId: string;
  turnId: string;
  itemId: string;
  target: "agent" | "plan" | "reasoningSummary" | "reasoningContent";
  index: number;
  delta: string;
};

type Buffer = Omit<StreamingDelta, "delta"> & { chunks: string[] };
type FlushWaiter = {
  matches: (buffer: Buffer) => boolean;
  resolve: () => void;
};

export class StreamingTextQueue {
  private readonly buffers = new Map<string, Buffer>();
  private readonly waiters = new Set<FlushWaiter>();
  private framePending = false;

  constructor(private readonly apply: (delta: StreamingDelta) => void,
    private readonly scheduleFrame: (callback: () => void) => void = defaultScheduleFrame) {}

  enqueue(delta: StreamingDelta): void {
    if (!delta.delta) return;
    const key = streamKey(delta);
    const current = this.buffers.get(key);
    if (current) current.chunks.push(delta.delta);
    else this.buffers.set(key, { ...delta, chunks: [delta.delta] });
    this.ensureFrame();
  }

  flushItem(threadId: string, turnId: string, itemId: string): Promise<void> {
    return this.flush((buffer) => buffer.threadId === threadId && buffer.turnId === turnId &&
      buffer.itemId === itemId);
  }

  flushTurn(threadId: string, turnId: string): Promise<void> {
    return this.flush((buffer) => buffer.threadId === threadId && buffer.turnId === turnId);
  }

  discardItem(threadId: string, turnId: string, itemId: string): void {
    this.discard((buffer) => buffer.threadId === threadId && buffer.turnId === turnId &&
      buffer.itemId === itemId);
  }

  discardTurn(threadId: string, turnId: string): void {
    this.discard((buffer) => buffer.threadId === threadId && buffer.turnId === turnId);
  }

  private flush(matches: (buffer: Buffer) => boolean): Promise<void> {
    if (![...this.buffers.values()].some(matches)) return Promise.resolve();
    return new Promise<void>((resolve) => {
      this.waiters.add({ matches, resolve });
      this.ensureFrame();
    });
  }

  private discard(matches: (buffer: Buffer) => boolean): void {
    for (const [key, buffer] of this.buffers) {
      if (matches(buffer)) this.buffers.delete(key);
    }
    this.settleWaiters();
  }

  private ensureFrame(): void {
    if (this.framePending || this.buffers.size === 0) return;
    this.framePending = true;
    this.scheduleFrame(() => this.onFrame());
  }

  private onFrame(): void {
    this.framePending = false;
    for (const [key, buffer] of this.buffers) {
      this.buffers.delete(key);
      const delta = buffer.chunks.join("");
      if (delta) this.apply({ ...buffer, delta });
    }
    this.settleWaiters();
    if (this.buffers.size > 0) this.ensureFrame();
  }

  private settleWaiters(): void {
    for (const waiter of [...this.waiters]) {
      const pending = [...this.buffers.values()].some(waiter.matches);
      if (pending) continue;
      this.waiters.delete(waiter);
      waiter.resolve();
    }
  }
}

export function streamKey(delta: Omit<StreamingDelta, "delta">): string {
  return `${delta.threadId}:${delta.turnId}:${delta.itemId}:${delta.target}:${delta.index}`;
}

function defaultScheduleFrame(callback: () => void): void {
  if (typeof requestAnimationFrame === "function") requestAnimationFrame(() => callback());
  else setTimeout(callback, 16);
}
