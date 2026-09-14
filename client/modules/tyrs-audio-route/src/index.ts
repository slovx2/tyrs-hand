import { requireNativeModule, type EventSubscription } from "expo-modules-core";
import { Platform } from "react-native";

export type LiveAudioRouteKind = "bluetooth" | "wired" | "speaker" | "earpiece" | "unknown";

export type LiveAudioRouteDevice = {
  id: number;
  name: string;
  type: number;
  kind: LiveAudioRouteKind;
};

export type LiveAudioRoute = {
  kind: LiveAudioRouteKind;
  automatic: boolean;
  activeDevice: LiveAudioRouteDevice | null;
  activeInputDevice: LiveAudioRouteDevice | null;
  availableDevices: LiveAudioRouteDevice[];
  audioMode: number;
  speakerphoneOn: boolean;
  bluetoothScoOn: boolean;
  bluetoothCommunicationAvailable: boolean;
};

type NativeAudioRoute = {
  prepareLiveAudioRoute(): Promise<LiveAudioRoute>;
  setLiveAudioRoute(kind: "auto" | LiveAudioRouteKind, deviceId?: number | null): Promise<LiveAudioRoute>;
  restoreLiveAudioRoute(): Promise<void>;
  getLiveAudioRoute(): Promise<LiveAudioRoute>;
  playLiveCue(kind: "connecting" | "connected"): Promise<void>;
  addListener(eventName: "onLiveAudioRouteChanged",
    listener: (route: LiveAudioRoute) => void): EventSubscription;
};

const fallbackRoute: LiveAudioRoute = {
  kind: "unknown",
  automatic: true,
  activeDevice: null,
  activeInputDevice: null,
  availableDevices: [],
  audioMode: 0,
  speakerphoneOn: false,
  bluetoothScoOn: false,
  bluetoothCommunicationAvailable: false,
};

function nativeModule(): NativeAudioRoute {
  return requireNativeModule<NativeAudioRoute>("TyrsAudioRoute");
}

export default {
  prepareLiveAudioRoute(): Promise<LiveAudioRoute> {
    if (Platform.OS !== "android") return Promise.resolve(fallbackRoute);
    return nativeModule().prepareLiveAudioRoute();
  },
  setLiveAudioRoute(kind: "auto" | LiveAudioRouteKind, deviceId?: number | null): Promise<LiveAudioRoute> {
    if (Platform.OS !== "android") return Promise.resolve(fallbackRoute);
    return nativeModule().setLiveAudioRoute(kind, deviceId);
  },
  restoreLiveAudioRoute(): Promise<void> {
    if (Platform.OS !== "android") return Promise.resolve();
    return nativeModule().restoreLiveAudioRoute();
  },
  getLiveAudioRoute(): Promise<LiveAudioRoute> {
    if (Platform.OS !== "android") return Promise.resolve(fallbackRoute);
    return nativeModule().getLiveAudioRoute();
  },
  addLiveAudioRouteListener(listener: (route: LiveAudioRoute) => void): EventSubscription | null {
    if (Platform.OS !== "android") return null;
    return nativeModule().addListener("onLiveAudioRouteChanged", listener);
  },
  playLiveCue(kind: "connecting" | "connected"): Promise<void> {
    if (Platform.OS !== "android") return Promise.resolve();
    return nativeModule().playLiveCue(kind);
  },
};
