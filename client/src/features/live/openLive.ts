import { router } from "expo-router";
import { Platform } from "react-native";

import { finishLiveActivity, openLiveActivity } from "@/native/voiceWake";

export function openLive(wake = false): void {
  if (Platform.OS === "android") {
    void openLiveActivity(wake);
    return;
  }
  router.push({ pathname: "/live", params: wake ? { wake: "1" } : {} } as never);
}

export function leaveLive(): void {
  if (Platform.OS === "android") {
    void finishLiveActivity();
    return;
  }
  if (router.canGoBack()) router.back();
  else router.replace("/(tabs)/sessions");
}
