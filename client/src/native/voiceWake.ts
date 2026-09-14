import { requireNativeModule, type EventSubscription } from "expo-modules-core";
import { Platform } from "react-native";

type NativeVoiceWake = {
  isDefaultAssistant(): Promise<boolean>;
  openAssistantSettings(): Promise<void>;
  openLive(wake: boolean): Promise<void>;
  finishLive(): Promise<void>;
  takeLiveWake(): Promise<boolean>;
  addListener(eventName: "onLiveWake", listener: () => void): EventSubscription;
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

export function openLiveActivity(wake: boolean): Promise<void> {
  if (Platform.OS !== "android") return Promise.resolve();
  return nativeModule().openLive(wake);
}

export function finishLiveActivity(): Promise<void> {
  if (Platform.OS !== "android") return Promise.resolve();
  return nativeModule().finishLive();
}

export function takeLiveWake(): Promise<boolean> {
  if (Platform.OS !== "android") return Promise.resolve(false);
  return nativeModule().takeLiveWake();
}

export function addLiveWakeListener(listener: () => void): EventSubscription | null {
  if (Platform.OS !== "android") return null;
  return nativeModule().addListener("onLiveWake", listener);
}
