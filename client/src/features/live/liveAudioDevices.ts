import type { LiveAudioRoute, LiveAudioRouteDevice } from "tyrs-audio-route";

export function selectableLiveAudioDevices(route: LiveAudioRoute | null): LiveAudioRouteDevice[] {
  if (!route) return [];
  const devices = [
    ...route.availableDevices,
    ...(route.activeDevice ? [route.activeDevice] : []),
  ];
  return devices.filter((device, index) =>
    devices.findIndex((candidate) => candidate.id === device.id) === index &&
    device.kind !== "unknown" && device.kind !== "earpiece");
}
