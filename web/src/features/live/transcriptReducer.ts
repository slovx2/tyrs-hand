export type LiveTranscriptItem = {
  role: 'user' | 'assistant'
  text: string
  eventId?: string
}
export type LiveTranscriptState = {
  items: LiveTranscriptItem[]
  partial: Record<string, LiveTranscriptItem>
}
export const initialLiveTranscriptState: LiveTranscriptState = {
  items: [],
  partial: {},
}
export type LiveTranscriptEvent = {
  type?: string
  id?: string
  item_id?: string
  response_id?: string
  delta?: string
  text?: string
  transcript?: string
}

function withEventId(
  item: LiveTranscriptItem,
  eventId?: string,
): LiveTranscriptItem {
  return eventId ? { ...item, eventId } : item
}

export function reduceLiveTranscript(
  state: LiveTranscriptState,
  event: LiveTranscriptEvent,
): LiveTranscriptState {
  const type = event.type ?? ''
  const role = type.includes('input') ? 'user' : 'assistant'
  const key = event.item_id ?? event.response_id ?? role
  if (type.endsWith('.delta')) {
    const previous = state.partial[key]?.text ?? ''
    return {
      ...state,
      partial: {
        ...state.partial,
        [key]: withEventId(
          { role, text: previous + (event.delta ?? '') },
          event.id,
        ),
      },
    }
  }
  if (!type.endsWith('.done') && !type.endsWith('.completed')) return state
  const item = state.partial[key]
  const text = event.text ?? event.transcript ?? item?.text ?? ''
  if (!text) return state
  const partial = { ...state.partial }
  delete partial[key]
  return {
    items: [
      ...state.items,
      withEventId({ role, text }, event.id ?? item?.eventId),
    ],
    partial,
  }
}

export function visibleLiveTranscript(
  state: LiveTranscriptState,
): LiveTranscriptItem[] {
  return [...state.items, ...Object.values(state.partial)]
}
