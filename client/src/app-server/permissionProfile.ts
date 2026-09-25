import type { AskForApproval } from "@codex-app-server/v2/AskForApproval";
import type { SandboxPolicy } from "@codex-app-server/v2/SandboxPolicy";

export const PERMISSION_PROFILES = [
  { id: ":danger-full-access", label: "完全访问" },
  { id: ":workspace", label: "工作区" },
  { id: ":read-only", label: "只读" },
] as const;

export type PermissionProfile = (typeof PERMISSION_PROFILES)[number]["id"];
export const DEFAULT_PERMISSION_PROFILE: PermissionProfile = ":danger-full-access";

export function isPermissionProfile(value: unknown): value is PermissionProfile {
  return PERMISSION_PROFILES.some((item) => item.id === value);
}

export function normalizePermissionProfile(value: unknown): PermissionProfile {
  if (value == null) return DEFAULT_PERMISSION_PROFILE;
  return isPermissionProfile(value) ? value : ":read-only";
}

export function permissionProfileLabel(value: PermissionProfile): string {
  return PERMISSION_PROFILES.find((item) => item.id === value)?.label ?? "完全访问";
}

export function permissionProfileTestID(value: PermissionProfile): string {
  return `parameters:permissions:${value.slice(1)}`;
}

export function permissionProfileFromRuntime(
  profile: { id?: string } | null | undefined,
  sandbox?: { type?: string } | null,
): PermissionProfile {
  if (isPermissionProfile(profile?.id)) return profile.id;
  switch (sandbox?.type) {
  case "dangerFullAccess":
    return ":danger-full-access";
  case "readOnly":
    return ":read-only";
  case "workspaceWrite":
    return ":workspace";
  default:
    return ":read-only";
  }
}

export type RuntimePermissions = { approvalPolicy: AskForApproval; sandboxPolicy: SandboxPolicy };

// 非内置组合必须保留原始策略，不能把“全磁盘且需审批”恢复成“完全访问免审批”。
export function runtimePermissionPreferences(profile: { id?: string } | null | undefined,
  sandbox: SandboxPolicy, approvalPolicy: AskForApproval): {
    permissions: PermissionProfile; runtimePermissions?: RuntimePermissions;
  } {
  const permissions = permissionProfileFromRuntime(profile, sandbox);
  const canonical = (permissions === ":danger-full-access" && approvalPolicy === "never" &&
    sandbox?.type === "dangerFullAccess") ||
    (permissions === ":read-only" && approvalPolicy === "on-request" &&
      sandbox?.type === "readOnly" && sandbox.networkAccess === false);
  // 工作区包含可写目录、网络及临时目录边界，不能只按 type 折叠为内置档位。
  return canonical ? { permissions } : { permissions,
    runtimePermissions: { approvalPolicy, sandboxPolicy: sandbox } };
}

export function turnPermissionParams(value: { permissions: PermissionProfile;
  runtimePermissions?: RuntimePermissions | undefined }) {
  return value.runtimePermissions ?? { permissions: normalizePermissionProfile(value.permissions) };
}
