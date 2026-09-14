import { router, useLocalSearchParams } from "expo-router";
import { useEffect } from "react";
import { Platform } from "react-native";

import { LiveScreen } from "@/features/live/LiveScreen";
import { leaveLive, openLive } from "@/features/live/openLive";
import { isLiveWakeParam } from "@/features/live/liveWake";

export default function LiveRoute() {
  const params = useLocalSearchParams<Record<string, string | string[]>>();
  const wake = isLiveWakeParam(params.wake);
  useEffect(() => {
    if (Platform.OS !== "android") return;
    openLive(wake);
    if (router.canGoBack()) router.back();
    else router.replace("/(tabs)/sessions");
  }, [wake]);
  if (Platform.OS === "android") return null;
  return <LiveScreen initialWake={wake} onLeave={leaveLive} />;
}
