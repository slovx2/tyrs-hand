import { StatusBar } from "expo-status-bar";
import { type ReactNode, useEffect, useRef } from "react";
import { AppState } from "react-native";

import { closeOfficialProfile } from "@/app-server/registry";
import { createActiveTurnReconciler } from "@/features/chat/activeTurnReconciler";
import { sshTransport } from "@/native/sshTransport";
import { useAppStore } from "@/store/appStore";
import { useTheme } from "@/theme/ThemeProvider";

export function AppRuntime({ children }: { children: ReactNode }) {
  const theme = useTheme();
  const initialize = useAppStore((state) => state.initialize);
  const refresh = useAppStore((state) => state.refresh);
  const refreshActiveThreads = useAppStore((state) => state.refreshActiveThreads);
  const refreshRecentThreads = useAppStore((state) => state.refreshRecentThreads);
  const retryOutbox = useAppStore((state) => state.retryOutbox);
  const outboxCount = useAppStore((state) => state.outbox.length);
  const connection = useAppStore((state) => state.activeConnection);
  const started = useRef(false);
  useEffect(() => {
    if (started.current) return;
    started.current = true;
    void initialize();
  }, [initialize]);
  useEffect(() => {
    if (!connection) return;
    const activeThreads = createActiveTurnReconciler(refreshActiveThreads, 1_800);
    let recentThreadsTimer: ReturnType<typeof setInterval> | null = null;
    const startForegroundSync = () => {
      activeThreads.start();
      if (!recentThreadsTimer) {
        recentThreadsTimer = setInterval(() => {
          void refreshRecentThreads().catch(() => undefined);
        }, 5_000);
      }
    };
    const stopForegroundSync = () => {
      activeThreads.stop();
      if (recentThreadsTimer) clearInterval(recentThreadsTimer);
      recentThreadsTimer = null;
    };
    if (AppState.currentState === "active") startForegroundSync();
    const subscription = AppState.addEventListener("change", (state) => {
      if (state === "active") {
        startForegroundSync();
        void refresh();
        return;
      }
      stopForegroundSync();
      closeOfficialProfile(connection.profileId);
      void sshTransport.close(connection.profileId).catch(() => undefined);
    });
    return () => {
      subscription.remove();
      stopForegroundSync();
      activeThreads.dispose();
      closeOfficialProfile(connection.profileId);
      void sshTransport.close(connection.profileId).catch(() => undefined);
    };
  }, [connection, refresh, refreshActiveThreads, refreshRecentThreads]);
  useEffect(() => {
    if (!connection || outboxCount === 0) return;
    const timer = setInterval(() => {
      void retryOutbox().catch(() => undefined);
    }, 15_000);
    return () => clearInterval(timer);
  }, [connection, outboxCount, retryOutbox]);
  return <><StatusBar style={theme.dark ? "light" : "dark"} />{children}</>;
}
