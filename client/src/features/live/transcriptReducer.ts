export type LiveTranscriptItem = {
  role: "user" | "assistant"
  text: string
  eventId?: string
}

export type LiveTranscriptState = {
  items: LiveTranscriptItem[]
  partial: Record<string, LiveTranscriptItem>
  seenEventIds: Record<string, true>
  finalized: Record<string, true>
}

export const initialLiveTranscriptState: LiveTranscriptState = {
  items: [],
  partial: {},
  seenEventIds: {},
  finalized: {},
}

export type LiveTranscriptEvent = Record<string, unknown> & {
  type?: string
  id?: string
  event_id?: string
  eventId?: string
  item_id?: string
  itemId?: string
  response_id?: string
  responseId?: string
}

type TranscriptKind = {
  role: "user" | "assistant"
  phase: "delta" | "added" | "done"
}

function eventId(event: LiveTranscriptEvent): string | undefined {
  const value = event.id ?? event.event_id ?? event.eventId
  return typeof value === "string" && value.trim() ? value.trim() : undefined
}

function nonEmptyString(value: unknown): string | undefined {
  return typeof value === "string" && value.trim() ? value : undefined
}

function nestedField(value: unknown, field: string): string | undefined {
  if (!value || typeof value !== "object") return undefined
  if (Array.isArray(value)) {
    for (const child of value) {
      const result = nestedField(child, field)
      if (result !== undefined) return result
    }
    return undefined
  }
  const object = value as Record<string, unknown>
  const direct = nonEmptyString(object[field])
  if (direct !== undefined) return direct
  for (const child of Object.values(object)) {
    const result = nestedField(child, field)
    if (result !== undefined) return result
  }
  return undefined
}

function nestedIdentity(value: unknown): string | undefined {
  if (!value || typeof value !== "object") return undefined
  if (Array.isArray(value)) {
    for (const child of value) {
      const result = nestedIdentity(child)
      if (result !== undefined) return result
    }
    return undefined
  }
  const object = value as Record<string, unknown>
  for (const field of ["item_id", "itemId", "response_id", "responseId", "id"]) {
    const result = nonEmptyString(object[field])
    if (result !== undefined) return result.trim()
  }
  for (const child of Object.values(object)) {
    const result = nestedIdentity(child)
    if (result !== undefined) return result
  }
  return undefined
}

function transcriptKind(type: string): TranscriptKind | null {
  const lower = type.toLowerCase()
  if (lower.includes("audio") && !lower.includes("transcript") && !lower.includes("transcription")) return null
  const role = lower.includes("input") ? "user" : lower.includes("output") ? "assistant" : null
  if (!role) return null
  if (lower.endsWith(".delta")) return { role, phase: "delta" }
  if (lower.endsWith(".added")) return { role, phase: "added" }
  if (lower.endsWith(".done") || lower.endsWith(".completed")) return { role, phase: "done" }
  return null
}

function transcriptKey(event: LiveTranscriptEvent, role: TranscriptKind["role"], type: string): string {
  const identity = event.item_id ?? event.itemId ?? event.response_id ?? event.responseId ??
    nestedIdentity(event.item) ?? nestedIdentity(event.response) ?? nestedIdentity(event.turn)
  if (typeof identity === "string" && identity.trim()) return `${role}:${identity.trim()}`
  return `${role}:${type.toLowerCase().replace(/\.(delta|done|completed|added)$/, "")}`
}

function withEventId(item: LiveTranscriptItem, id?: string): LiveTranscriptItem {
  return id ? { ...item, eventId: id } : item
}

function markSeen(state: LiveTranscriptState, id?: string): Record<string, true> {
  return id ? { ...state.seenEventIds, [id]: true } : state.seenEventIds
}

function flushPartials(state: LiveTranscriptState, seenEventIds: Record<string, true>): LiveTranscriptState {
  const items = [...state.items]
  const finalized = { ...state.finalized }
  const partial = { ...state.partial }
  for (const [key, item] of Object.entries(partial)) {
    if (item.text) {
      items.push(item)
      finalized[key] = true
    }
    delete partial[key]
  }
  return { items, partial, seenEventIds, finalized }
}

export function reduceLiveTranscript(state: LiveTranscriptState, event: LiveTranscriptEvent): LiveTranscriptState {
  const id = eventId(event)
  if (id && state.seenEventIds[id]) return state
  const seenEventIds = markSeen(state, id)
  const type = typeof event.type === "string" ? event.type : ""
  const lowerType = type.toLowerCase()
  if (lowerType === "turn.done" || lowerType.endsWith(".turn.done")) {
    return flushPartials(state, seenEventIds)
  }
  const kind = transcriptKind(type)
  if (!kind) return id ? { ...state, seenEventIds } : state
  const key = transcriptKey(event, kind.role, type)
  if (state.finalized[key] && !state.partial[key]) return { ...state, seenEventIds }
  if (kind.phase === "delta") {
    const delta = nestedField(event, "delta")
    if (delta === undefined) return { ...state, seenEventIds }
    return {
      ...state,
      seenEventIds,
      partial: {
        ...state.partial,
        [key]: withEventId({ role: kind.role, text: (state.partial[key]?.text ?? "") + delta }, id),
      },
    }
  }
  const partial = state.partial[key]
  const text = nestedField(event, "text") ?? nestedField(event, "transcript") ?? nestedField(event, "delta") ?? partial?.text ?? ""
  const nextPartial = { ...state.partial }
  delete nextPartial[key]
  if (!text) return { ...state, seenEventIds, partial: nextPartial }
  return {
    items: [...state.items, withEventId({ role: kind.role, text }, id ?? partial?.eventId)],
    partial: nextPartial,
    seenEventIds,
    finalized: { ...state.finalized, [key]: true },
  }
}

export function visibleLiveTranscript(state: LiveTranscriptState): LiveTranscriptItem[] {
  return [...state.items, ...Object.values(state.partial)]
}
