import type { Model } from "@codex-app-server/v2/Model";

export const defaultLiveCodexModel = "gpt-5.6-luna";
export const defaultLiveCodexEffort = "medium";

export type LiveCodexPreferences = {
  model: string;
  effort: string;
};

export function resolveLiveCodexPreferences(models: Model[],
  stored: LiveCodexPreferences | null): LiveCodexPreferences {
  const visible = models.filter((item) => !item.hidden);
  const preferred = stored ?? { model: defaultLiveCodexModel, effort: defaultLiveCodexEffort };
  const selected = visible.find((item) => item.id === preferred.model)
    ?? visible.find((item) => item.id === defaultLiveCodexModel)
    ?? visible[0];
  if (!selected) return preferred;
  const efforts = selected.supportedReasoningEfforts.map((item) => item.reasoningEffort);
  const effort = efforts.includes(preferred.effort as typeof efforts[number]) ? preferred.effort
    : efforts.includes(defaultLiveCodexEffort as typeof efforts[number]) ? defaultLiveCodexEffort
      : selected.defaultReasoningEffort;
  return { model: selected.id, effort };
}
