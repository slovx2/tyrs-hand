import { Audio, InterruptionModeAndroid, InterruptionModeIOS } from "expo-av";
import audioRoute from "tyrs-audio-route";

import connectingSound from "../../../assets/live-sounds/live-connect-start.wav";
import connectedSound from "../../../assets/live-sounds/live-connect-ready.wav";

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
  // 原生路由模块选择通信设备，不强制使用手机听筒。
  playThroughEarpieceAndroid: false,
};

let soundQueue: Promise<void> = Promise.resolve();

export function playLiveConnectionSound(kind: LiveConnectionSound,
  enabled: boolean): Promise<void> {
  if (!enabled) return Promise.resolve();
  const operation = soundQueue.catch(() => undefined).then(async () => {
    let sound: Audio.Sound | null = null;
    try {
      try { await Audio.setAudioModeAsync(liveAudioMode); } catch { /* 提示音不阻断建连。 */ }
      try { await audioRoute.prepareLiveAudioRoute(); } catch { /* 使用系统当前路由。 */ }
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
      if (!status.isLoaded || status.didJustFinish) finish();
    });
  });
}
