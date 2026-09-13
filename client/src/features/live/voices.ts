import arborPreview from "../../../assets/live-voices/arbor.wav";
import breezePreview from "../../../assets/live-voices/breeze.wav";
import covePreview from "../../../assets/live-voices/cove.wav";
import emberPreview from "../../../assets/live-voices/ember.wav";
import juniperPreview from "../../../assets/live-voices/juniper.wav";
import maplePreview from "../../../assets/live-voices/maple.wav";
import solPreview from "../../../assets/live-voices/sol.wav";
import sprucePreview from "../../../assets/live-voices/spruce.wav";
import valePreview from "../../../assets/live-voices/vale.wav";

export const liveVoices = [
  { slug: "arbor", name: "Arbor", description: "随和多变", preview: arborPreview },
  { slug: "breeze", name: "Breeze", description: "活泼真挚", preview: breezePreview },
  { slug: "cove", name: "Cove", description: "沉稳直接", preview: covePreview },
  { slug: "ember", name: "Ember", description: "自信乐观", preview: emberPreview },
  { slug: "juniper", name: "Juniper", description: "开放明快", preview: juniperPreview },
  { slug: "maple", name: "Maple", description: "亲切坦率", preview: maplePreview },
  { slug: "sol", name: "Sol", description: "干练放松", preview: solPreview },
  { slug: "spruce", name: "Spruce", description: "冷静可靠", preview: sprucePreview },
  { slug: "vale", name: "Vale", description: "明亮好奇", preview: valePreview },
] as const;

export type LiveVoice = (typeof liveVoices)[number]["slug"];
export const defaultLiveVoice: LiveVoice = "cove";

export function findLiveVoice(value: string | undefined): (typeof liveVoices)[number] {
  return liveVoices.find((voice) => voice.slug === value) ?? liveVoices[2];
}
