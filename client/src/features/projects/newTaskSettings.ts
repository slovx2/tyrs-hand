import type { Bootstrap, Project, SessionSettings } from "@/types/protocol";

export function resolveNewTaskSettings(bootstrap: Bootstrap, project: Project): SessionSettings | null {
  const remembered = bootstrap.lastStartedSettings;
  const profile = bootstrap.agentProfiles.find((item) => item.id === remembered?.agentProfileId) ??
    bootstrap.agentProfiles[0];
  if (!profile) return null;

  const models = (bootstrap.modelCatalogs[project.workspaceId]?.data ?? [])
    .filter((item) => !item.hidden);
  const rememberedModelAvailable = remembered?.model === null ||
    models.some((item) => item.id === remembered?.model);
  if (remembered && (models.length === 0 || rememberedModelAvailable)) {
    return { ...remembered, agentProfileId: profile.id };
  }

  const model = models.find((item) => item.isDefault) ?? models[0];
  if (model) {
    return {
      agentProfileId: profile.id,
      model: model.id,
      reasoningEffort: model.defaultReasoningEffort,
      serviceTier: model.defaultServiceTier === "priority" || model.defaultServiceTier === "fast" ?
        "fast" : "standard",
      collaborationMode: "default",
      settingsVersion: 0,
    };
  }

  return {
    agentProfileId: profile.id,
    model: remembered?.model ?? null,
    reasoningEffort: remembered?.reasoningEffort ?? null,
    serviceTier: remembered?.serviceTier ?? "standard",
    collaborationMode: remembered?.collaborationMode ?? "default",
    settingsVersion: remembered?.settingsVersion ?? 0,
  };
}
