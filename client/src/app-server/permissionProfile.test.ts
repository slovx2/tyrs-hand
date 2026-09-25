import { describe, expect, it } from "vitest";

import { DEFAULT_PERMISSION_PROFILE, normalizePermissionProfile,
  permissionProfileFromRuntime, permissionProfileLabel, permissionProfileTestID,
  runtimePermissionPreferences, turnPermissionParams,
} from "./permissionProfile";

describe("permissionProfile", () => {
  it("新会话缺省完全访问，未知权限保守限制", () => {
    expect(normalizePermissionProfile(undefined)).toBe(DEFAULT_PERMISSION_PROFILE);
    expect(normalizePermissionProfile(":workspace")).toBe(":workspace");
    expect(normalizePermissionProfile("full-access")).toBe(":read-only");
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

  it("恢复全磁盘但需审批的会话后，继续提交保留原策略", () => {
    const selected = runtimePermissionPreferences(null, { type: "dangerFullAccess" }, "on-request");
    expect(selected.runtimePermissions).toEqual({ approvalPolicy: "on-request",
      sandboxPolicy: { type: "dangerFullAccess" } });
    expect(turnPermissionParams(selected)).toEqual(selected.runtimePermissions);
    expect(turnPermissionParams({ ...selected, runtimePermissions: undefined }))
      .toEqual({ permissions: ":danger-full-access", approvalPolicy: "never" });
    expect(turnPermissionParams({ permissions: ":workspace" }))
      .toEqual({ permissions: ":workspace", approvalPolicy: "on-request" });
    expect(turnPermissionParams({ permissions: ":read-only" }))
      .toEqual({ permissions: ":read-only", approvalPolicy: "on-request" });
  });

  it("完全访问标准组合仍使用内置权限，未知运行时不会放大授权", () => {
    expect(runtimePermissionPreferences({ id: ":danger-full-access" },
      { type: "dangerFullAccess" }, "never")).toEqual({ permissions: ":danger-full-access" });
    expect(permissionProfileFromRuntime({ id: "unknown" }, { type: "unknown" }))
      .toBe(":read-only");
  });

  it("接力工作区保留目录、网络和临时目录边界", () => {
    const sandbox = { type: "workspaceWrite" as const, writableRoots: ["/workspace/review"],
      networkAccess: false, excludeTmpdirEnvVar: true, excludeSlashTmp: true };
    const selected = runtimePermissionPreferences({ id: ":workspace" }, sandbox, "on-request");
    expect(turnPermissionParams(selected)).toEqual({ approvalPolicy: "on-request",
      sandboxPolicy: sandbox });
    const readOnly = { type: "readOnly" as const, networkAccess: true };
    expect(turnPermissionParams(runtimePermissionPreferences(null, readOnly, "on-request")))
      .toEqual({ approvalPolicy: "on-request", sandboxPolicy: readOnly });
  });
});
