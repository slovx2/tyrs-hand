import * as Notifications from "expo-notifications";
import { router } from "expo-router";
import { StatusBar } from "expo-status-bar";
import { type ReactNode, useEffect, useRef } from "react";
import { AppState } from "react-native";

import type { Connection } from "@/db/connections";
import { registerPush } from "@/notifications/register";
import { useAppStore } from "@/store/appStore";
import { processOutbox, recoverFailedOutbox } from "@/sync/outbox";
import { subscribeToUpdates, Synchronizer } from "@/sync/synchronizer";
import { useTheme } from "@/theme/ThemeProvider";

export function AppRuntime({ children }: { children: ReactNode }) {
  const theme = useTheme();
  const initialize = useAppStore((state) => state.initialize);
  const refresh = useAppStore((state) => state.refresh);
  const connection = useAppStore((state) => state.activeConnection);
  const started = useRef(false);
  useEffect(() => {
    if (started.current) return;
    started.current = true;
    void initialize();
  }, [initialize]);
  useEffect(() => {
    if (!connection) return;
    const synchronizer = new Synchronizer(connection, refresh);
    void synchronizer.start().catch(() => undefined);
    void resumeOutbox(connection).then(refresh).catch(() => undefined);
    void registerPush(connection).catch(() => undefined);
    const unsubscribeUpdates = subscribeToUpdates((event) => {
      if (event.kind === "durable") void refresh();
    });
    const subscription = AppState.addEventListener("change", (state) => {
      if (state === "active") {
        void synchronizer.start().catch(() => undefined);
        void resumeOutbox(connection).then(refresh).catch(() => undefined);
      }
    });
    return () => { synchronizer.stop(); unsubscribeUpdates(); subscription.remove(); };
  }, [connection, refresh]);
  useEffect(() => Notifications.addNotificationResponseReceivedListener((response) => {
    const data = response.notification.request.content.data as { serverId?: string; sessionId?: string };
    if (!data.serverId || !data.sessionId) return;
    void useAppStore.getState().switchConnection(data.serverId).then(() => {
      router.push({ pathname: "/session/[id]", params: { id: data.sessionId } });
    });
  }).remove, []);
  return <><StatusBar style={theme.dark ? "light" : "dark"} />{children}</>;
}

async function resumeOutbox(connection: Connection) {
  await recoverFailedOutbox(connection.serverId);
  await processOutbox(connection);
}
