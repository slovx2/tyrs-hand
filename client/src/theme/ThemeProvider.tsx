import { createContext, type ReactNode, useContext, useMemo } from "react";
import { useColorScheme } from "react-native";

import { useAppStore } from "@/store/appStore";
import { darkTheme, lightTheme, type Theme } from "./tokens";

const ThemeContext = createContext<Theme>(lightTheme);

export function ThemeProvider({ children }: { children: ReactNode }) {
  const system = useColorScheme();
  const mode = useAppStore((state) => state.themeMode);
  const dark = mode === "dark" || (mode === "system" && system === "dark");
  const theme = useMemo(() => (dark ? darkTheme : lightTheme), [dark]);
  return <ThemeContext.Provider value={theme}>{children}</ThemeContext.Provider>;
}

export function useTheme(): Theme {
  return useContext(ThemeContext);
}
