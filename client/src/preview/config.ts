export const isPreviewMode = process.env.EXPO_PUBLIC_TYRS_HAND_PREVIEW === "true";
export const isPreviewStressMode = process.env.EXPO_PUBLIC_TYRS_HAND_PREVIEW_STRESS === "true";
export const previewLatencyMs = Number(process.env.EXPO_PUBLIC_TYRS_HAND_PREVIEW_LATENCY_MS ?? "0");
export const previewPerfLogging = process.env.EXPO_PUBLIC_TYRS_HAND_PREVIEW_PERF === "true";

export const primaryPreviewServerId = "10000000-0000-4000-8000-000000000001";
export const secondaryPreviewServerId = "10000000-0000-4000-8000-000000000002";

export function isPreviewServerId(serverId: string): boolean {
  return serverId === primaryPreviewServerId || serverId === secondaryPreviewServerId;
}
