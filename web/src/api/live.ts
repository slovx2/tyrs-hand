import { api, jsonBody, type ListResponse } from './client'

export interface LiveConversation {
  id: string
  model: string
  voice: string
  instructions: string
  status: string
  activeSessionId?: string
  contextRevision: number
  lastError?: string
  createdAt: string
  updatedAt: string
}
export interface LiveSessionResponse {
  conversationId: string
  sessionId: string
  transport: { type: 'webrtc'; answerSdp: string }
  session: { status: string }
}
export interface LiveMessage {
  sequence: number
  role: string
  text: string
  sourceSessionId?: string
  createdAt: string
}
export interface LiveEvent {
  id: number
  direction: string
  type: string
  eventId?: string
  payload: Record<string, unknown>
  createdAt: string
}

export function createLiveConversation(input: {
  model?: string
  voice?: string
  instructions?: string
}) {
  return api<LiveConversation>('/client/live-conversations', {
    method: 'POST',
    ...jsonBody(input),
  })
}
export function getLiveConversation(id: string) {
  return api<LiveConversation>(`/client/live-conversations/${id}`)
}
export function createLiveSession(id: string, offerSdp: string) {
  return api<LiveSessionResponse>(`/client/live-conversations/${id}/sessions`, {
    method: 'POST',
    ...jsonBody({ offerSdp, platform: 'web' }),
  })
}
export function recoverLiveSession(id: string, offerSdp: string) {
  return api<LiveSessionResponse>(`/client/live-conversations/${id}/recover`, {
    method: 'POST',
    ...jsonBody({ offerSdp, platform: 'web' }),
  })
}
export function closeLiveSession(id: string) {
  return api<{ sessionId: string; status: string }>(
    `/client/live-sessions/${id}/close`,
    { method: 'POST' },
  )
}
export function listLiveMessages(id: string) {
  return api<ListResponse<LiveMessage>>(
    `/client/live-conversations/${id}/messages?limit=100`,
  )
}
export function listLiveEvents(id: string) {
  return api<ListResponse<LiveEvent>>(
    `/client/live-conversations/${id}/events?limit=100`,
  )
}
