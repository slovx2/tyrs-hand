import { describe, expect, it } from "vitest";
import type { Model } from "@codex-app-server/v2/Model";

import { defaultLiveCodexEffort, defaultLiveCodexModel, resolveLiveCodexPreferences } from "./liveCodexPreferences";

function model(id: string, efforts: string[], extra: Partial<Model> = {}): Model {
  return {
    id, model: id, upgrade: null, upgradeInfo: null, availabilityNux: null,
    displayName: id, description: id, modelSpecialty: null, hidden: false,
    supportedReasoningEfforts: efforts.map((reasoningEffort) => ({ reasoningEffort, description: reasoningEffort })),
    defaultReasoningEffort: efforts[0] ?? "medium", inputModalities: [], supportsPersonality: false,
    additionalSpeedTiers: [], serviceTiers: [], defaultServiceTier: null, isDefault: false, ...extra,
  } as Model;
}

describe("resolveLiveCodexPreferences", () => {
  it("defaults to luna medium when catalog is empty", () => {
    expect(resolveLiveCodexPreferences([], null)).toEqual({
      model: defaultLiveCodexModel, effort: defaultLiveCodexEffort,
    });
  });

  it("keeps stored model when it exists in the worker catalog", () => {
    const models = [model("gpt-5.6-sol", ["medium", "high", "xhigh"]), model("gpt-5.6-luna", ["low", "medium"])];
    expect(resolveLiveCodexPreferences(models, { model: "gpt-5.6-sol", effort: "high" }))
      .toEqual({ model: "gpt-5.6-sol", effort: "high" });
  });

  it("falls back to luna medium when stored model is missing", () => {
    const models = [model("gpt-5.6-luna", ["low", "medium", "high"]), model("gpt-5.6-sol", ["xhigh"])];
    expect(resolveLiveCodexPreferences(models, { model: "missing", effort: "xhigh" }))
      .toEqual({ model: "gpt-5.6-luna", effort: "medium" });
  });
});
