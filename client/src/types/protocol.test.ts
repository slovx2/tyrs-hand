import { describe, expect, it } from "vitest";

import { bootstrapSchema, sessionSettingsSchema } from "./protocol";

describe("client protocol v2", () => {
  it("接受 max 和 ultra 推理等级", () => {
    const base = { agentProfileId: "c64fddac-6a28-45ff-ab54-254930abbf1d", model: "gpt-5.6-sol",
      serviceTier: "standard", collaborationMode: "default", settingsVersion: 1 };
    expect(sessionSettingsSchema.parse({ ...base, reasoningEffort: "max" }).reasoningEffort).toBe("max");
    expect(sessionSettingsSchema.parse({ ...base, reasoningEffort: "ultra" }).reasoningEffort).toBe("ultra");
  });

  it("拒绝旧协议 Bootstrap", () => {
    expect(() => bootstrapSchema.parse({ protocolVersion: 1 })).toThrow();
  });
});
