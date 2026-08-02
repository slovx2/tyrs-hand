import { Tabs } from "expo-router";
import { Text } from "react-native";

import { useTablet } from "@/hooks/useTablet";
import { useTheme } from "@/theme/ThemeProvider";

const icon = (value: string, color: string) => <Text style={{ color, fontSize: 18 }}>{value}</Text>;

export default function TabLayout() {
  const tablet = useTablet();
  const theme = useTheme();
  return <Tabs screenOptions={{ headerShown: true, tabBarPosition: tablet ? "left" : "bottom",
    tabBarHideOnKeyboard: !tablet,
    tabBarStyle: { backgroundColor: theme.colors.rail, borderColor: theme.colors.border,
      ...(tablet ? { width: 88 } : {}) }, tabBarActiveTintColor: theme.colors.accent,
    tabBarInactiveTintColor: theme.colors.textMuted, headerStyle: { backgroundColor: theme.colors.surface },
    headerTintColor: theme.colors.text }}>
    <Tabs.Screen name="projects" options={{ title: "项目", tabBarButtonTestID: "tab:projects",
      tabBarIcon: ({ color }) => icon("⌘", color) }} />
    <Tabs.Screen name="sessions" options={{ title: "会话", tabBarButtonTestID: "tab:sessions",
      tabBarIcon: ({ color }) => icon("◫", color) }} />
    <Tabs.Screen name="connections" options={{ title: "连接", tabBarButtonTestID: "tab:connections",
      tabBarIcon: ({ color }) => icon("◎", color) }} />
  </Tabs>;
}
