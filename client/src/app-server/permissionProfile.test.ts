import { describe, expect, it } from "vitest";

import { DEFAULT_PERMISSION_PROFILE, normalizePermissionProfile,
  permissionProfileFromRuntime, permissionProfileLabel, permissionProfileTestID,
} from "./permissionProfile";

describe("permissionProfile", () => {
  it("缺省和未知值回退到完全访问", () => {
    expect(normalizePermissionProfile(undefined)).toBe(DEFAULT_PERMISSION_PROFILE);
    expect(normalizePermissionProfile(":workspace")).toBe(":workspace");
    expect(normalizePermissionProfile("full-access")).toBe(":danger-full-access");
  });

  it("从 Thread runtime 还原权限档", () => {
    expect(permissionProfileFromRuntime({ id: ":read-only" })).toBe(":read-only");
    expect(permissionProfileFromRuntime(null, { type: "workspaceWrite" })).toBe(":workspace");
    expect(permissionProfileFromRuntime(null, { type: "dangerFullAccess" }))
      .toBe(":danger-full-access");
  });

  it("权限档有稳定文案和测试 ID", () => {
    expect(permissionProfileLabel(":danger-full-access")).toBe("完全访问");
    expect(permissionProfileTestID(":workspace")).toBe("parameters:permissions:workspace");
  });
});
