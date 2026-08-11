const MAX_DISCLOSURES = 512;
export const ACTIVITY_TOGGLE_SCROLL_SETTLE_MS = 180;
const turnExpanded = new Map<string, boolean>();
const toolExpanded = new Map<string, boolean>();

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

export function isToolGroupExpanded(key: string): boolean {
  return toolExpanded.get(key) ?? false;
}

export function toggleToolGroup(key: string): boolean {
  const expanded = !isToolGroupExpanded(key);
  remember(toolExpanded, key, expanded);
  return expanded;
}

export function toolDisclosureExpanded(expanded: boolean | undefined): boolean {
  return expanded ?? false;
}

export function toggleToolDisclosure(expanded: boolean | undefined): boolean {
  return !toolDisclosureExpanded(expanded);
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
