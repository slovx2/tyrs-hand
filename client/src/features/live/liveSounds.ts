import { Audio, InterruptionModeAndroid, InterruptionModeIOS } from "expo-av";
import { Platform } from "react-native";
import audioRoute from "tyrs-audio-route";

import connectingSound from "../../../assets/live-sounds/live-connect-start.wav";
import connectedSound from "../../../assets/live-sounds/live-connect-ready.wav";
import { isPlaybackFinishedStatus, shouldPlayLiveConnectionSound, shouldUseNativeLiveCue } from "./liveSoundPolicy";

export type LiveConnectionSound = "connecting" | "connected";

const sources: Record<LiveConnectionSound, number> = {
  connecting: connectingSound,
  connected: connectedSound,
};

const liveAudioMode = {
  allowsRecordingIOS: false,
  interruptionModeIOS: InterruptionModeIOS.DoNotMix,
  playsInSilentModeIOS: true,
  staysActiveInBackground: false,
  interruptionModeAndroid: InterruptionModeAndroid.DoNotMix,
  shouldDuckAndroid: false,
  playThroughEarpieceAndroid: false,
};

let soundQueue: Promise<void> = Promise.resolve();

export function playLiveConnectionSound(kind: LiveConnectionSound,
  enabled: boolean): Promise<void> {
  if (!shouldPlayLiveConnectionSound(enabled)) return Promise.resolve();
  const operation = soundQueue.catch(() => undefined).then(async () => {
    if (shouldUseNativeLiveCue(Platform.OS)) {
      try { await audioRoute.playLiveCue(kind); } catch { /* 提示音不阻断建连。 */ }
      return;
    }
    let sound: Audio.Sound | null = null;
    try {
      try { await Audio.setAudioModeAsync(liveAudioMode); } catch { /* 提示音不阻断建连。 */ }
      sound = (await Audio.Sound.createAsync(sources[kind], { shouldPlay: true })).sound;
      await waitForSoundToFinish(sound);
    } catch {
      // 提示音是辅助反馈，播放失败不能阻断 Live 建连。
    } finally {
      if (sound) {
        try { await sound.unloadAsync(); } catch { /* 已结束的声音无需重复释放。 */ }
      }
    }
  });
  soundQueue = operation;
  return operation;
}

function waitForSoundToFinish(sound: Audio.Sound): Promise<void> {
  return new Promise((resolve) => {
    let finished = false;
    let timeout: ReturnType<typeof setTimeout>;
    const finish = () => {
      if (finished) return;
      finished = true;
      clearTimeout(timeout);
      sound.setOnPlaybackStatusUpdate(null);
      resolve();
    };
    timeout = setTimeout(finish, 1800);
    sound.setOnPlaybackStatusUpdate((status) => {
      if (isPlaybackFinishedStatus(status)) finish();
    });
  });
}
