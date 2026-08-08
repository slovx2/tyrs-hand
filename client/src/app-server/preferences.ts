import type { Model } from "@codex-app-server/v2/Model";

import type { TurnPreferences } from "./officialClient";

export function defaultTurnPreferences(models: Model[]): TurnPreferences | null {
  const model = models.find((item) => item.isDefault && !item.hidden) ??
    models.find((item) => !item.hidden);
  if (!model) return null;
  return { model: model.id, effort: model.defaultReasoningEffort,
    serviceTier: model.defaultServiceTier, collaborationMode: "default" };
}

export function resolveNewTaskPreferences(models: Model[],
  remembered: TurnPreferences | null): TurnPreferences | null {
  const visible = models.filter((model) => !model.hidden);
  if (remembered && (visible.length === 0 || visible.some((model) => model.id === remembered.model))) {
    return remembered;
  }
  return defaultTurnPreferences(models);
}
