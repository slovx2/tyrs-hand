import arborPreviewUrl from '../../assets/live-voices/arbor.wav?url'
import breezePreviewUrl from '../../assets/live-voices/breeze.wav?url'
import covePreviewUrl from '../../assets/live-voices/cove.wav?url'
import emberPreviewUrl from '../../assets/live-voices/ember.wav?url'
import juniperPreviewUrl from '../../assets/live-voices/juniper.wav?url'
import maplePreviewUrl from '../../assets/live-voices/maple.wav?url'
import solPreviewUrl from '../../assets/live-voices/sol.wav?url'
import sprucePreviewUrl from '../../assets/live-voices/spruce.wav?url'
import valePreviewUrl from '../../assets/live-voices/vale.wav?url'

export const liveVoices = [
  {
    slug: 'arbor',
    name: 'Arbor',
    description: '随和多变',
    previewUrl: arborPreviewUrl,
  },
  {
    slug: 'breeze',
    name: 'Breeze',
    description: '活泼真挚',
    previewUrl: breezePreviewUrl,
  },
  {
    slug: 'cove',
    name: 'Cove',
    description: '沉稳直接',
    previewUrl: covePreviewUrl,
  },
  {
    slug: 'ember',
    name: 'Ember',
    description: '自信乐观',
    previewUrl: emberPreviewUrl,
  },
  {
    slug: 'juniper',
    name: 'Juniper',
    description: '开放明快',
    previewUrl: juniperPreviewUrl,
  },
  {
    slug: 'maple',
    name: 'Maple',
    description: '亲切坦率',
    previewUrl: maplePreviewUrl,
  },
  {
    slug: 'sol',
    name: 'Sol',
    description: '干练放松',
    previewUrl: solPreviewUrl,
  },
  {
    slug: 'spruce',
    name: 'Spruce',
    description: '冷静可靠',
    previewUrl: sprucePreviewUrl,
  },
  {
    slug: 'vale',
    name: 'Vale',
    description: '明亮好奇',
    previewUrl: valePreviewUrl,
  },
] as const

export type LiveVoice = (typeof liveVoices)[number]['slug']

export const defaultLiveVoice: LiveVoice = 'cove'

export function findLiveVoice(value: string | undefined): (typeof liveVoices)[number] {
  return liveVoices.find((voice) => voice.slug === value) ?? liveVoices[2]
}
