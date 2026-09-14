import { StatusBar } from "expo-status-bar";
import { type ReactNode, useEffect } from "react";

import { useAppStore } from "@/store/appStore";
import { useTheme } from "@/theme/ThemeProvider";

export function LiveRuntime({ children }: { children: ReactNode }) {
  const theme = useTheme();
  const ready = useAppStore((state) => state.ready);
  const initialize = useAppStore((state) => state.initialize);
  useEffect(() => {
    if (!ready) void initialize();
  }, [initialize, ready]);
  return <><StatusBar style={theme.dark ? "light" : "dark"} />{children}</>;
}
