import { router } from "expo-router";

import { useAppStore } from "@/store/appStore";

export type NotificationTarget = {
  serverId?: unknown;
  sessionId?: unknown;
};

export function parseNotificationUrl(value: string): NotificationTarget | null {
  try {
    const url = new URL(value);
    const route = url.hostname || url.pathname.replace(/^\/+/, "");
    if (url.protocol !== "tyrshand:" || route !== "notification") return null;
    return {
      serverId: url.searchParams.get("serverId") ?? undefined,
      sessionId: url.searchParams.get("sessionId") ?? undefined,
    };
  } catch {
    return null;
  }
}

export async function openNotificationTarget(data: NotificationTarget): Promise<boolean> {
  if (typeof data.serverId !== "string" || typeof data.sessionId !== "string" ||
    !data.serverId || !data.sessionId) return false;
  const state = useAppStore.getState();
  if (!state.connections.some((connection) => connection.serverId === data.serverId)) return false;
  await state.switchConnection(data.serverId);
  router.replace({ pathname: "/session/[id]", params: { id: data.sessionId } });
  return true;
}
