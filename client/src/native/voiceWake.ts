import { requireNativeModule } from "expo-modules-core";
import { Platform } from "react-native";

type NativeVoiceWake = {
  isDefaultAssistant(): Promise<boolean>;
  openAssistantSettings(): Promise<void>;
};

function nativeModule(): NativeVoiceWake {
  return requireNativeModule<NativeVoiceWake>("TyrsVoice");
}

export function isDefaultAssistant(): Promise<boolean> {
  if (Platform.OS !== "android") return Promise.resolve(false);
  return nativeModule().isDefaultAssistant();
}

export function openAssistantSettings(): Promise<void> {
  if (Platform.OS !== "android") return Promise.resolve();
  return nativeModule().openAssistantSettings();
}
