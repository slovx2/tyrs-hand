export type LiveTranscriptItem = {
  role: 'user' | 'assistant'
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
  turn_id?: string
  turnId?: string
  role?: string
  turn?: unknown
}

type TranscriptKind = {
  role: 'user' | 'assistant'
  phase: 'delta' | 'added' | 'done'
}

function eventId(event: LiveTranscriptEvent): string | undefined {
  const value = event.id ?? event.event_id ?? event.eventId
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function nonEmptyString(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value : undefined
}

function nestedField(value: unknown, field: string): string | undefined {
  if (!value || typeof value !== 'object') return undefined
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
  if (!value || typeof value !== 'object') return undefined
  if (Array.isArray(value)) {
    for (const child of value) {
      const result = nestedIdentity(child)
      if (result !== undefined) return result
    }
    return undefined
  }
  const object = value as Record<string, unknown>
  for (const field of [
    'item_id',
    'itemId',
    'response_id',
    'responseId',
    'id',
  ]) {
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
  if (
    lower.includes('audio') &&
    !lower.includes('transcript') &&
    !lower.includes('transcription')
  )
    return null
  const role = lower.includes('input')
    ? 'user'
    : lower.includes('output')
      ? 'assistant'
      : null
  if (!role) return null
  if (lower.endsWith('.delta')) return { role, phase: 'delta' }
  if (lower.endsWith('.added')) return { role, phase: 'added' }
  if (lower.endsWith('.done') || lower.endsWith('.completed'))
    return { role, phase: 'done' }
  return null
}

function eventTurnId(event: LiveTranscriptEvent): string | undefined {
  if (event.turn && typeof event.turn === 'object' && !Array.isArray(event.turn)) {
    const id = nonEmptyString((event.turn as Record<string, unknown>).id)
    if (id) return id.trim()
  }
  const value = event.turn_id ?? event.turnId
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function eventTurnRole(
  event: LiveTranscriptEvent,
): LiveTranscriptItem['role'] | undefined {
  let role: unknown
  if (event.turn && typeof event.turn === 'object' && !Array.isArray(event.turn)) {
    role = (event.turn as Record<string, unknown>).role
  }
  if (role === undefined) role = event.role
  return role === 'user' || role === 'assistant' ? role : undefined
}

function eventTurnTranscript(event: LiveTranscriptEvent): string | undefined {
  if (!event.turn || typeof event.turn !== 'object' || Array.isArray(event.turn)) {
    return undefined
  }
  const object = event.turn as Record<string, unknown>
  return nonEmptyString(object.transcript) ?? nonEmptyString(object.text)
}

function isTurnType(
  lowerType: string,
  phase: 'created' | 'delta' | 'done',
): boolean {
  return lowerType === `turn.${phase}` || lowerType.endsWith(`.turn.${phase}`)
}

function transcriptKey(
  event: LiveTranscriptEvent,
  role: TranscriptKind['role'],
  type: string,
  phase: TranscriptKind['phase'],
): string {
  const turnId = eventTurnId(event)
  if (turnId) return `${role}:turn:${turnId}`
  if (phase === 'added') return `${role}:open`
  const identity =
    event.item_id ??
    event.itemId ??
    event.response_id ??
    event.responseId ??
    nestedIdentity(event.item) ??
    nestedIdentity(event.response)
  if (typeof identity === 'string' && identity.trim())
    return `${role}:${identity.trim()}`
  return `${role}:${type.toLowerCase().replace(/\.(delta|done|completed|added)$/, '')}`
}

function withEventId(
  item: LiveTranscriptItem,
  id?: string,
): LiveTranscriptItem {
  return id ? { ...item, eventId: id } : item
}

function markSeen(
  state: LiveTranscriptState,
  id?: string,
): Record<string, true> {
  return id ? { ...state.seenEventIds, [id]: true } : state.seenEventIds
}

function activeTurnKey(
  state: LiveTranscriptState,
  role: LiveTranscriptItem['role'],
): string | undefined {
  return Object.keys(state.partial).find((key) => key.startsWith(`${role}:turn:`))
}

function findTurnPartialKey(
  state: LiveTranscriptState,
  role: LiveTranscriptItem['role'] | undefined,
  turnId: string | undefined,
): string | undefined {
  if (role && turnId) {
    const key = `${role}:turn:${turnId}`
    if (state.partial[key]) return key
  }
  if (turnId) {
    const suffix = `:turn:${turnId}`
    const match = Object.keys(state.partial).find((key) => key.endsWith(suffix))
    if (match) return match
  }
  return role ? activeTurnKey(state, role) : undefined
}

function finalizeTurn(
  state: LiveTranscriptState,
  event: LiveTranscriptEvent,
  seenEventIds: Record<string, true>,
  id?: string,
): LiveTranscriptState {
  const role = eventTurnRole(event)
  const turnId = eventTurnId(event)
  const transcript = eventTurnTranscript(event)
  const turnKey =
    findTurnPartialKey(state, role, turnId) ??
    (role && turnId ? `${role}:turn:${turnId}` : undefined)
  const openKey = role ? `${role}:open` : undefined
  const text =
    transcript ??
    (turnKey ? state.partial[turnKey]?.text : undefined) ??
    (openKey ? state.partial[openKey]?.text : undefined) ??
    ''
  const items = [...state.items]
  const partial = { ...state.partial }
  const finalized = { ...state.finalized }
  if (text && role) {
    items.push(
      withEventId(
        { role, text },
        id ??
          (turnKey ? state.partial[turnKey]?.eventId : undefined) ??
          (openKey ? state.partial[openKey]?.eventId : undefined),
      ),
    )
  }
  if (turnKey) {
    delete partial[turnKey]
    finalized[turnKey] = true
  }
  if (openKey) {
    delete partial[openKey]
    finalized[openKey] = true
  }
  return { items, partial, seenEventIds, finalized }
}

export function reduceLiveTranscript(
  state: LiveTranscriptState,
  event: LiveTranscriptEvent,
): LiveTranscriptState {
  const id = eventId(event)
  if (id && state.seenEventIds[id]) return state
  const seenEventIds = markSeen(state, id)
  const type = typeof event.type === 'string' ? event.type : ''
  const lowerType = type.toLowerCase()
  if (isTurnType(lowerType, 'created')) {
    const role = eventTurnRole(event)
    const turnId = eventTurnId(event)
    if (!role || !turnId) return id ? { ...state, seenEventIds } : state
    const key = `${role}:turn:${turnId}`
    const openKey = `${role}:open`
    const text = eventTurnTranscript(event) ?? state.partial[openKey]?.text ?? ''
    const partial = { ...state.partial }
    delete partial[openKey]
    if (text) {
      partial[key] = withEventId(
        { role, text },
        id ?? state.partial[openKey]?.eventId,
      )
    }
    return { ...state, seenEventIds, partial }
  }
  if (isTurnType(lowerType, 'delta')) {
    const delta = nestedField(event, 'delta')
    if (delta === undefined) return { ...state, seenEventIds }
    const role = eventTurnRole(event)
    const turnId = eventTurnId(event)
    const key =
      findTurnPartialKey(state, role, turnId) ??
      (role && turnId ? `${role}:turn:${turnId}` : undefined)
    if (!key) return { ...state, seenEventIds }
    const existing = state.partial[key]
    const nextRole = existing?.role ?? role
    if (!nextRole) return { ...state, seenEventIds }
    return {
      ...state,
      seenEventIds,
      partial: {
        ...state.partial,
        [key]: withEventId(
          { role: nextRole, text: (existing?.text ?? '') + delta },
          id ?? existing?.eventId,
        ),
      },
    }
  }
  if (isTurnType(lowerType, 'done')) {
    return finalizeTurn(state, event, seenEventIds, id)
  }
  const kind = transcriptKind(type)
  if (!kind) return id ? { ...state, seenEventIds } : state
  const key = transcriptKey(event, kind.role, type, kind.phase)
  if (state.finalized[key] && !state.partial[key])
    return { ...state, seenEventIds }
  if (kind.phase === 'delta' || kind.phase === 'added') {
    if (
      kind.phase === 'added' &&
      activeTurnKey(state, kind.role) &&
      key === `${kind.role}:open`
    ) {
      return { ...state, seenEventIds }
    }
    const piece =
      kind.phase === 'delta'
        ? nestedField(event, 'delta')
        : (nestedField(event, 'delta') ??
          nestedField(event, 'text') ??
          nestedField(event, 'transcript'))
    if (piece === undefined) return { ...state, seenEventIds }
    return {
      ...state,
      seenEventIds,
      partial: {
        ...state.partial,
        [key]: withEventId(
          { role: kind.role, text: (state.partial[key]?.text ?? '') + piece },
          id,
        ),
      },
    }
  }
  const partial = state.partial[key]
  const text =
    nestedField(event, 'text') ??
    nestedField(event, 'transcript') ??
    nestedField(event, 'delta') ??
    partial?.text ??
    ''
  const nextPartial = { ...state.partial }
  delete nextPartial[key]
  if (!text) return { ...state, seenEventIds, partial: nextPartial }
  return {
    items: [
      ...state.items,
      withEventId({ role: kind.role, text }, id ?? partial?.eventId),
    ],
    partial: nextPartial,
    seenEventIds,
    finalized: { ...state.finalized, [key]: true },
  }
}

export function visibleLiveTranscript(
  state: LiveTranscriptState,
): LiveTranscriptItem[] {
  return [...state.items, ...Object.values(state.partial)]
}
