import { useSyncExternalStore } from "react";
import { AccessibilityInfo } from "react-native";

let currentValue = false;
let subscription: { remove: () => void } | null = null;
const listeners = new Set<() => void>();

export function useReducedMotion(): boolean {
  return useSyncExternalStore(subscribe, getSnapshot, getSnapshot);
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  if (!subscription) {
    subscription = AccessibilityInfo.addEventListener("reduceMotionChanged", updateValue);
    void AccessibilityInfo.isReduceMotionEnabled().then(updateValue).catch(() => undefined);
  }
  return () => {
    listeners.delete(listener);
    if (listeners.size === 0) {
      subscription?.remove();
      subscription = null;
    }
  };
}

function getSnapshot(): boolean {
  return currentValue;
}

function updateValue(value: boolean): void {
  if (currentValue === value) return;
  currentValue = value;
  for (const listener of listeners) listener();
}
