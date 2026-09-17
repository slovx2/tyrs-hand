import type { Model } from "@codex-app-server/v2/Model";

import type { TurnPreferences } from "./officialClient";
import { DEFAULT_PERMISSION_PROFILE, normalizePermissionProfile,
  permissionProfileLabel } from "./permissionProfile";

export function defaultTurnPreferences(models: Model[]): TurnPreferences | null {
  const model = models.find((item) => item.isDefault && !item.hidden) ??
    models.find((item) => !item.hidden);
  if (!model) return null;
  return { model: model.id, effort: model.defaultReasoningEffort,
    serviceTier: model.defaultServiceTier, collaborationMode: "default",
    permissions: DEFAULT_PERMISSION_PROFILE };
}

export function normalizeTurnPreferences(value: TurnPreferences): TurnPreferences {
  return { ...value, permissions: normalizePermissionProfile(value.permissions) };
}

export function turnPreferencesSummary(value: TurnPreferences): string {
  const normalized = normalizeTurnPreferences(value);
  const mode = normalized.collaborationMode === "plan" ? "先做计划" : "直接执行";
  return `${normalized.model} · ${normalized.effort ?? "默认"} · ${permissionProfileLabel(
    normalized.permissions)} · ${mode}`;
}

export function resolveNewTaskPreferences(models: Model[],
  remembered: TurnPreferences | null): TurnPreferences | null {
  const visible = models.filter((model) => !model.hidden);
  if (remembered && (visible.length === 0 || visible.some((model) => model.id === remembered.model))) {
    return { ...remembered, permissions: normalizePermissionProfile(remembered.permissions) };
  }
  return defaultTurnPreferences(models);
}
