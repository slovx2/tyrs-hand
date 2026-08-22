import { StatusBar } from "expo-status-bar";
import { type ReactNode, useEffect, useRef } from "react";
import { AppState } from "react-native";

import { closeOfficialProfile } from "@/app-server/registry";
import { sshTransport } from "@/native/sshTransport";
import { useAppStore } from "@/store/appStore";
import { useTheme } from "@/theme/ThemeProvider";
import { RenderSchedulerProvider } from "@/render/renderScheduler";

export function AppRuntime({ children }: { children: ReactNode }) {
  const theme = useTheme();
  const initialize = useAppStore((state) => state.initialize);
  const refresh = useAppStore((state) => state.refresh);
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
    const subscription = AppState.addEventListener("change", (state) => {
      if (state === "active") {
        void refresh();
        return;
      }
      closeOfficialProfile(connection.profileId);
      void sshTransport.close(connection.profileId).catch(() => undefined);
    });
    return () => subscription.remove();
  }, [connection, refresh]);
  useEffect(() => {
    if (!connection || outboxCount === 0) return;
    const timer = setInterval(() => {
      void retryOutbox().catch(() => undefined);
    }, 15_000);
    return () => clearInterval(timer);
  }, [connection, outboxCount, retryOutbox]);
  return <RenderSchedulerProvider><StatusBar style={theme.dark ? "light" : "dark"} />{children}</RenderSchedulerProvider>;
}
