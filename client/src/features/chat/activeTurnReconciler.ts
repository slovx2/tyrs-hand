export type ActiveTurnReconciler = {
  start: () => void;
  stop: () => void;
  dispose: () => void;
};

export function createActiveTurnReconciler(reconcile: () => Promise<void>,
  intervalMs = 1_800): ActiveTurnReconciler {
  let running = false;
  let disposed = false;
  let generation = 0;
  let timer: ReturnType<typeof setTimeout> | null = null;

  const stop = () => {
    running = false;
    generation += 1;
    if (timer) clearTimeout(timer);
    timer = null;
  };

  const run = async (currentGeneration: number): Promise<void> => {
    try {
      await reconcile();
    } catch {
      // 通知仍是低延迟主路径；短暂断线由下一轮权威读取继续修复。
    }
    if (!running || disposed || generation !== currentGeneration) return;
    timer = setTimeout(() => {
      timer = null;
      void run(currentGeneration);
    }, intervalMs);
  };

  return {
    start: () => {
      if (running || disposed) return;
      running = true;
      const currentGeneration = ++generation;
      void run(currentGeneration);
    },
    stop,
    dispose: () => {
      stop();
      disposed = true;
    },
  };
}
