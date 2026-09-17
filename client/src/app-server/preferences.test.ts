import type { Model } from "@codex-app-server/v2/Model";
import { describe, expect, it } from "vitest";

import type { TurnPreferences } from "./officialClient";
import { resolveNewTaskPreferences, turnPreferencesSummary } from "./preferences";

const remembered: TurnPreferences = { model: "gpt-remembered", effort: "max",
  serviceTier: "fast", collaborationMode: "plan", permissions: ":workspace" };

function model(id: string, isDefault = false): Model {
  return { id, model: id, upgrade: null, upgradeInfo: null, availabilityNux: null,
    displayName: id, description: id, modelSpecialty: null, hidden: false,
    supportedReasoningEfforts: [{ reasoningEffort: "high", description: "high" }],
    defaultReasoningEffort: "high", inputModalities: ["text"], supportsPersonality: false,
    additionalSpeedTiers: [], serviceTiers: [{ id: "fast", name: "快速", description: "快速" }],
    defaultServiceTier: "standard", isDefault };
}

describe("resolveNewTaskPreferences", () => {
  it("当前目录仍包含上次模型时保留该 profile 最近启动参数", () => {
    expect(resolveNewTaskPreferences([model("gpt-remembered", true)], remembered))
      .toEqual(remembered);
  });

  it("模型目录暂时为空时仍保留最近参数", () => {
    expect(resolveNewTaskPreferences([], remembered)).toEqual(remembered);
  });

  it("最近模型已失效时回退到当前官方默认模型", () => {
    expect(resolveNewTaskPreferences([model("gpt-current", true)], remembered)).toEqual({
      model: "gpt-current", effort: "high", serviceTier: "standard",
      collaborationMode: "default", permissions: ":danger-full-access",
    });
  });

  it("缺少 permissions 的旧参数补成完全访问", () => {
    const legacy = { model: "gpt-remembered", effort: "max" as const, serviceTier: "fast",
      collaborationMode: "plan" as const };
    expect(resolveNewTaskPreferences([model("gpt-remembered")],
      legacy as TurnPreferences)?.permissions).toBe(":danger-full-access");
  });
});

describe("turnPreferencesSummary", () => {
  it("参数摘要包含权限档", () => {
    expect(turnPreferencesSummary(remembered)).toBe("gpt-remembered · max · 工作区 · 先做计划");
  });
});
