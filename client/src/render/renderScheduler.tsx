import { createContext, useContext, useMemo, type ReactNode } from "react";
import { InteractionManager } from "react-native";

export type RenderPriority = "transition" | "user" | "background";

type RenderTask = {
  priority: RenderPriority;
  run: () => void;
};

export type RenderScheduler = {
  schedule: (task: () => void, priority?: RenderPriority) => () => void;
  afterInteractions: (task: () => void) => () => void;
};

const queues: Record<RenderPriority, RenderTask[]> = {
  transition: [], user: [], background: [],
};
let scheduled = false;

function flush(): void {
  scheduled = false;
  const tasks = [
    ...queues.user.splice(0),
    ...queues.transition.splice(0, 2),
    ...queues.background.splice(0, 1),
  ];
  for (const task of tasks) task.run();
  if (queues.user.length + queues.transition.length + queues.background.length > 0) {
    scheduleFlush();
  }
}

function scheduleFlush(): void {
  if (scheduled) return;
  scheduled = true;
  if (typeof requestAnimationFrame === "function") requestAnimationFrame(flush);
  else setTimeout(flush, 16);
}

const scheduler: RenderScheduler = {
  schedule: (run, priority = "background") => {
    const task: RenderTask = { run, priority };
    queues[priority].push(task);
    scheduleFlush();
    return () => {
      const index = queues[priority].indexOf(task);
      if (index >= 0) queues[priority].splice(index, 1);
    };
  },
  afterInteractions: (run) => {
    const handle = InteractionManager.runAfterInteractions(run);
    return () => handle.cancel();
  },
};

const RenderSchedulerContext = createContext<RenderScheduler>(scheduler);

export function RenderSchedulerProvider({ children }: { children: ReactNode }) {
  const value = useMemo(() => scheduler, []);
  return <RenderSchedulerContext.Provider value={value}>{children}</RenderSchedulerContext.Provider>;
}

export function useRenderScheduler(): RenderScheduler {
  return useContext(RenderSchedulerContext);
}

export function scheduleRenderTask(task: () => void, priority?: RenderPriority): () => void {
  return scheduler.schedule(task, priority);
}
