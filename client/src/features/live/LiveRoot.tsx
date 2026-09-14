import { Inter_400Regular, Inter_500Medium, Inter_600SemiBold, useFonts } from "@expo-google-fonts/inter";
import { GestureHandlerRootView } from "react-native-gesture-handler";
import { SafeAreaProvider } from "react-native-safe-area-context";

import { LiveRuntime } from "./LiveRuntime";
import { LiveScreen } from "./LiveScreen";
import { leaveLive } from "./openLive";
import { ThemeProvider } from "@/theme/ThemeProvider";

export function LiveRoot() {
  const [loaded] = useFonts({ Inter_400Regular, Inter_500Medium, Inter_600SemiBold });
  if (!loaded) return null;
  return <GestureHandlerRootView style={{ flex: 1 }}>
    <SafeAreaProvider>
      <ThemeProvider>
        <LiveRuntime>
          <LiveScreen onLeave={leaveLive} />
        </LiveRuntime>
      </ThemeProvider>
    </SafeAreaProvider>
  </GestureHandlerRootView>;
}
