import * as Linking from "expo-linking";
import * as Notifications from "expo-notifications";
import { StatusBar } from "expo-status-bar";
import { type ReactNode, useEffect, useRef } from "react";
import { AppState } from "react-native";

import type { Connection } from "@/db/connections";
import { openNotificationTarget, parseNotificationUrl, type NotificationTarget } from "@/notifications/navigation";
import { registerPush } from "@/notifications/register";
import { isPreviewMode, isPreviewServerId } from "@/preview/config";
import { useAppStore } from "@/store/appStore";
import { processOutbox, recoverFailedOutbox } from "@/sync/outbox";
import { setAppActive } from "@/sync/appLifecycle";
import { subscribeToUpdates, Synchronizer } from "@/sync/synchronizer";
import { useTheme } from "@/theme/ThemeProvider";

export function AppRuntime({ children }: { children: ReactNode }) {
  const theme = useTheme();
  const initialize = useAppStore((state) => state.initialize);
  const refresh = useAppStore((state) => state.refresh);
  const ready = useAppStore((state) => state.ready);
  const connection = useAppStore((state) => state.activeConnection);
  const started = useRef(false);
  const pendingNotification = useRef<NotificationTarget | null>(null);
  useEffect(() => {
    if (started.current) return;
    started.current = true;
    void initialize();
  }, [initialize]);
  useEffect(() => {
    if (!connection) return;
    if (isPreviewMode && isPreviewServerId(connection.serverId)) return;
    const synchronizer = new Synchronizer(connection, refresh);
    void synchronizer.start().catch(() => undefined);
    void resumeOutbox(connection).then(refresh).catch(() => undefined);
    void registerPush(connection).catch(() => undefined);
    let refreshTimer: ReturnType<typeof setTimeout> | null = null;
    const scheduleRefresh = () => {
      if (refreshTimer) return;
      refreshTimer = setTimeout(() => { refreshTimer = null; void refresh(); }, 200);
    };
    const unsubscribeUpdates = subscribeToUpdates((event) => {
      if (event.kind === "durable") scheduleRefresh();
    });
    const subscription = AppState.addEventListener("change", (state) => {
      setAppActive(state === "active");
      if (state === "active") {
        void synchronizer.start().catch(() => undefined);
        void resumeOutbox(connection).then(refresh).catch(() => undefined);
      }
    });
    return () => {
      if (refreshTimer) clearTimeout(refreshTimer);
      synchronizer.stop(); unsubscribeUpdates(); subscription.remove();
    };
  }, [connection, refresh]);
  useEffect(() => {
    if (isPreviewMode) return;
    const openOrDefer = (target: NotificationTarget) => {
      if (!useAppStore.getState().ready) {
        pendingNotification.current = target;
        return;
      }
      void openNotificationTarget(target);
    };
    void Linking.getInitialURL().then((url) => {
      const target = url ? parseNotificationUrl(url) : null;
      if (target) openOrDefer(target);
    });
    const linkingSubscription = Linking.addEventListener("url", ({ url }) => {
      const target = parseNotificationUrl(url);
      if (target) openOrDefer(target);
    });
    void Notifications.getLastNotificationResponseAsync().then((response) => {
      if (response) openOrDefer(response.notification.request.content.data);
    });
    const notificationSubscription = Notifications.addNotificationResponseReceivedListener((response) => {
      openOrDefer(response.notification.request.content.data);
    });
    return () => {
      linkingSubscription.remove();
      notificationSubscription.remove();
    };
  }, []);
  useEffect(() => {
    if (!ready || !pendingNotification.current) return;
    const target = pendingNotification.current;
    pendingNotification.current = null;
    void openNotificationTarget(target);
  }, [ready]);
  return <><StatusBar style={theme.dark ? "light" : "dark"} />{children}</>;
}

async function resumeOutbox(connection: Connection) {
  await recoverFailedOutbox(connection.serverId);
  await processOutbox(connection);
}
