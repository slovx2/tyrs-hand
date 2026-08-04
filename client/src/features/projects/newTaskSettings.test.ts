import { describe, expect, it } from "vitest";

import type { Bootstrap, Project, SessionSettings } from "@/types/protocol";
import { resolveNewTaskSettings } from "./newTaskSettings";

const workspaceId = "00000000-0000-4000-8000-000000000001";
const profileId = "00000000-0000-4000-8000-000000000002";
const project: Project = {
  id: "00000000-0000-4000-8000-000000000003",
  workspaceId,
  name: "WakeQora",
  relativePath: "WakeQora",
  kind: "git",
  availabilityStatus: "available",
  branch: "main",
  dirty: false,
};
const remembered: SessionSettings = {
  agentProfileId: profileId,
  model: "gpt-5.6-sol",
  reasoningEffort: "high",
  serviceTier: "fast",
  collaborationMode: "default",
  settingsVersion: 4,
};

function bootstrap(overrides: Partial<Bootstrap> = {}): Bootstrap {
  return {
    serverId: "00000000-0000-4000-8000-000000000004",
    protocolVersion: 3,
    currentCursor: 0,
    user: { id: "00000000-0000-4000-8000-000000000005", username: "tester" },
    capabilities: {},
    projects: [project],
    agentProfiles: [{ id: profileId, name: "默认" }],
    modelCatalogs: {},
    lastStartedSettings: remembered,
    ...overrides,
  };
}

describe("resolveNewTaskSettings", () => {
  it("模型目录暂时为空时仍使用服务端记住的上次成功参数", () => {
    expect(resolveNewTaskSettings(bootstrap(), project)).toEqual(remembered);
  });

  it("没有历史参数和模型目录时使用服务端默认模型并保持任务可创建", () => {
    expect(resolveNewTaskSettings(bootstrap({ lastStartedSettings: null }), project)).toEqual({
      agentProfileId: profileId,
      model: null,
      reasoningEffort: null,
      serviceTier: "standard",
      collaborationMode: "default",
      settingsVersion: 0,
    });
  });

  it("目录可用且历史模型失效时回退到当前默认模型", () => {
    const result = resolveNewTaskSettings(bootstrap({ modelCatalogs: { [workspaceId]: {
      data: [{ id: "gpt-current", displayName: "Current", description: "current",
        inputModalities: ["text"],
        supportedReasoningEfforts: [{ reasoningEffort: "medium", description: "medium" }],
        defaultReasoningEffort: "medium", serviceTiers: [], additionalSpeedTiers: [],
        defaultServiceTier: null, isDefault: true, hidden: false }], nextCursor: null,
    } } }), project);
    expect(result?.model).toBe("gpt-current");
    expect(result?.reasoningEffort).toBe("medium");
  });
});
