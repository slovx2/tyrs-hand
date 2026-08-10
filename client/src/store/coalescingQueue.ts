export class CoalescingKeyedQueue {
  private readonly entries = new Map<string, {
    dirty: boolean;
    promise: Promise<void>;
  }>();

  run(key: string, operation: () => Promise<void>): Promise<void> {
    const active = this.entries.get(key);
    if (active) {
      active.dirty = true;
      return active.promise;
    }
    const entry = { dirty: false, promise: Promise.resolve() };
    entry.promise = (async () => {
      do {
        entry.dirty = false;
        await operation();
      } while (entry.dirty);
    })().finally(() => {
      if (this.entries.get(key) === entry) this.entries.delete(key);
    });
    this.entries.set(key, entry);
    return entry.promise;
  }
}
