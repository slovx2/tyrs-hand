export const isPreviewMode = process.env.EXPO_PUBLIC_TYRS_HAND_PREVIEW === "true";

export const primaryPreviewServerId = "10000000-0000-4000-8000-000000000001";
export const secondaryPreviewServerId = "10000000-0000-4000-8000-000000000002";

export function isPreviewServerId(serverId: string): boolean {
  return serverId === primaryPreviewServerId || serverId === secondaryPreviewServerId;
}
