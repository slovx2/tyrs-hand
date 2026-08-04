import { Inter_400Regular, Inter_500Medium, Inter_600SemiBold, useFonts } from "@expo-google-fonts/inter";
import { Stack } from "expo-router";
import * as SplashScreen from "expo-splash-screen";
import { useEffect } from "react";
import { GestureHandlerRootView } from "react-native-gesture-handler";
import { SafeAreaProvider } from "react-native-safe-area-context";

import { AppRuntime } from "@/components/AppRuntime";
import { ThemeProvider, useTheme } from "@/theme/ThemeProvider";

void SplashScreen.preventAutoHideAsync();

function Navigation() {
  const theme = useTheme();
  return <Stack screenOptions={{ headerStyle: { backgroundColor: theme.colors.surface },
    headerTintColor: theme.colors.text, headerBackTitle: "返回", contentStyle: { backgroundColor: theme.colors.app },
    gestureEnabled: true, fullScreenGestureEnabled: true }}>
    <Stack.Screen name="index" options={{ headerShown: false }} />
    <Stack.Screen name="device-pair" options={{ title: "添加 Control", gestureEnabled: false }} />
    <Stack.Screen name="notification" options={{ title: "打开通知" }} />
    <Stack.Screen name="(tabs)" options={{ headerShown: false }} />
    <Stack.Screen name="project/[id]" options={{ title: "项目会话" }} />
    <Stack.Screen name="project/[id]/new" options={{ title: "新任务" }} />
    <Stack.Screen name="session/[id]" options={{ title: "会话" }} />
  </Stack>;
}

export default function RootLayout() {
  const [loaded] = useFonts({ Inter_400Regular, Inter_500Medium, Inter_600SemiBold });
  useEffect(() => { if (loaded) void SplashScreen.hideAsync(); }, [loaded]);
  if (!loaded) return null;
  return <GestureHandlerRootView style={{ flex: 1 }}>
    <SafeAreaProvider><ThemeProvider><AppRuntime><Navigation /></AppRuntime></ThemeProvider></SafeAreaProvider>
  </GestureHandlerRootView>;
}
