export type ToolDisclosureMemory = {
  runningCollapsed: boolean;
  completedExpanded: boolean;
};

const MAX_DISCLOSURES = 512;
export const ACTIVITY_TOGGLE_SCROLL_SETTLE_MS = 180;
export const INITIAL_ACTIVITY_RENDER_COUNT = 6;
export const ACTIVITY_RENDER_BATCH_SIZE = 4;

export function nextActivityRenderCount(current: number, total: number): number {
  if (total <= 0) return 0;
  return Math.min(total, Math.max(INITIAL_ACTIVITY_RENDER_COUNT,
    current + ACTIVITY_RENDER_BATCH_SIZE));
}
const turnExpanded = new Map<string, boolean>();
const toolDisclosures = new Map<string, ToolDisclosureMemory>();

export function isTurnActivityCollapsed(key: string, canCollapse: boolean): boolean {
  if (!canCollapse) return false;
  return !(turnExpanded.get(key) ?? false);
}

export function toggleTurnActivity(key: string, canCollapse: boolean): boolean {
  const expanded = isTurnActivityCollapsed(key, canCollapse);
  remember(turnExpanded, key, expanded);
  return expanded;
}

export function activityToggleAllowed(blockedUntilMs: number, nowMs = Date.now()): boolean {
  return nowMs >= blockedUntilMs;
}

export function isToolGroupExpanded(key: string, running: boolean): boolean {
  return toolDisclosureExpanded(toolDisclosures.get(key), running);
}

export function toggleToolGroup(key: string, running: boolean): boolean {
  const memory = toolDisclosures.get(key) ?? emptyToolDisclosure();
  const expanded = toolDisclosureExpanded(memory, running);
  const next = running
    ? { ...memory, runningCollapsed: expanded }
    : { ...memory, completedExpanded: !expanded };
  remember(toolDisclosures, key, next);
  return toolDisclosureExpanded(next, running);
}

export function toolDisclosureExpanded(memory: ToolDisclosureMemory | undefined,
  running: boolean): boolean {
  const value = memory ?? emptyToolDisclosure();
  return running ? !value.runningCollapsed : value.completedExpanded;
}

export function toggleToolDisclosure(memory: ToolDisclosureMemory | undefined,
  running: boolean): ToolDisclosureMemory {
  const value = memory ?? emptyToolDisclosure();
  const expanded = toolDisclosureExpanded(value, running);
  return running
    ? { ...value, runningCollapsed: expanded }
    : { ...value, completedExpanded: !expanded };
}

function emptyToolDisclosure(): ToolDisclosureMemory {
  return { runningCollapsed: false, completedExpanded: false };
}

function remember<Value>(store: Map<string, Value>, key: string, value: Value): void {
  store.delete(key);
  store.set(key, value);
  while (store.size > MAX_DISCLOSURES) {
    const oldest = store.keys().next().value as string | undefined;
    if (oldest === undefined) return;
    store.delete(oldest);
  }
}
