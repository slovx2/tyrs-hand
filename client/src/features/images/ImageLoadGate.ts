import { createContext } from "react";

export type ImageLoadGate = {
  runWhenReady(task: () => void): () => void;
  setBlocked(blocked: boolean): void;
};

export const ImageLoadGateContext = createContext<ImageLoadGate | null>(null);

export function createImageLoadGate(initiallyBlocked = false): ImageLoadGate {
  let blocked = initiallyBlocked;
  const pending = new Set<() => void>();
  return {
    runWhenReady: (task) => {
      if (!blocked) {
        task();
        return () => undefined;
      }
      pending.add(task);
      return () => pending.delete(task);
    },
    setBlocked: (next) => {
      if (blocked === next) return;
      blocked = next;
      if (blocked) return;
      const tasks = [...pending];
      pending.clear();
      tasks.forEach((task) => task());
    },
  };
}
