export function shouldPlayLiveConnectionSound(enabled: boolean): boolean {
  return enabled;
}

export function shouldUseNativeLiveCue(os: string): boolean {
  return os === "android";
}

export function isPlaybackFinishedStatus(status: {
  isLoaded: boolean;
  didJustFinish?: boolean;
}): boolean {
  return status.isLoaded && status.didJustFinish === true;
}
