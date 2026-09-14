import { describe, expect, it } from "vitest";

import { selectableLiveAudioDevices } from "./liveAudioDevices";
import type { LiveAudioRoute } from "tyrs-audio-route";

function route(available: LiveAudioRoute["availableDevices"], active?: LiveAudioRoute["activeDevice"]): LiveAudioRoute {
  return {
    kind: active?.kind ?? "speaker", automatic: true, activeDevice: active ?? null,
    activeInputDevice: null, availableDevices: available, audioMode: 0,
    speakerphoneOn: true, bluetoothScoOn: false, bluetoothCommunicationAvailable: false,
  };
}

describe("selectableLiveAudioDevices", () => {
  it("drops earpiece and unknown devices", () => {
    const speaker = { id: 1, name: "Speaker", type: 2, kind: "speaker" as const };
    const earpiece = { id: 2, name: "Earpiece", type: 1, kind: "earpiece" as const };
    const unknown = { id: 3, name: "Other", type: 0, kind: "unknown" as const };
    expect(selectableLiveAudioDevices(route([speaker, earpiece, unknown], earpiece))).toEqual([speaker]);
  });
});
