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
  return isPermissionProfile(value) ? value : DEFAULT_PERMISSION_PROFILE;
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
    return DEFAULT_PERMISSION_PROFILE;
  }
}
