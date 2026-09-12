import { z } from "zod";

import { getControlDeviceToken, type ControlMachineLink } from "@/db/connections";

export const liveConversationSchema = z.object({
  id: z.string().uuid(), model: z.string(), voice: z.string(), instructions: z.string(),
  status: z.string(), activeSessionId: z.string().uuid().optional(), contextRevision: z.number(),
  lastError: z.string().optional(), createdAt: z.string(), updatedAt: z.string(),
});
export type LiveConversation = z.infer<typeof liveConversationSchema>;
export const liveSessionSchema = z.object({
  conversationId: z.string().uuid(), sessionId: z.string().uuid(),
  transport: z.object({ type: z.literal("webrtc"), answerSdp: z.string() }),
  session: z.object({ status: z.string() }),
});
export type LiveSession = z.infer<typeof liveSessionSchema>;
export type LiveMessage = { sequence: number; role: "developer" | "user" | "assistant"; text: string; sourceSessionId?: string; createdAt: string };
export type LiveEvent = { id: number; direction: string; type: string; eventId?: string; payload: Record<string, unknown>; createdAt: string };

async function controlRequest<T>(link: ControlMachineLink, path: string, init?: RequestInit, parse?: (value: unknown) => T): Promise<T> {
  const token = await getControlDeviceToken(link.serverId);
  if (!token) throw new Error("Control 凭证不存在，请重新授权设备");
  const response = await fetch(`${link.baseUrl.replace(/\/$/, "")}/api/v1/client${path}`, {
    ...init, headers: { Accept: "application/json", Authorization: `Bearer ${token}`, ...init?.headers },
  });
  if (!response.ok) { const problem = await response.json().catch(() => null) as { detail?: string; title?: string } | null; throw new Error(problem?.detail || problem?.title || `Control 请求失败（${response.status}）`); }
  const value = response.status === 204 ? undefined : await response.json();
  return parse ? parse(value) : value as T;
}
export function createLiveConversation(link: ControlMachineLink, input: { model?: string; voice?: string; instructions?: string } = {}) { return controlRequest(link, "/live-conversations", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(input) }, (value) => liveConversationSchema.parse(value)); }
export function getLiveConversation(link: ControlMachineLink, id: string) { return controlRequest(link, `/live-conversations/${id}`, undefined, (value) => liveConversationSchema.parse(value)); }
export function createLiveSession(link: ControlMachineLink, id: string, offerSdp: string) { return controlRequest(link, `/live-conversations/${id}/sessions`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ offerSdp, platform: "android" }) }, (value) => liveSessionSchema.parse(value)); }
export function recoverLiveSession(link: ControlMachineLink, id: string, offerSdp: string) { return controlRequest(link, `/live-conversations/${id}/recover`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ offerSdp, platform: "android" }) }, (value) => liveSessionSchema.parse(value)); }
export function closeLiveSession(link: ControlMachineLink, id: string) { return controlRequest(link, `/live-sessions/${id}/close`, { method: "POST" }, (value) => z.object({ sessionId: z.string().uuid(), status: z.string() }).parse(value)); }
export function listLiveMessages(link: ControlMachineLink, id: string) { return controlRequest<{ items: LiveMessage[] }>(link, `/live-conversations/${id}/messages?limit=100`); }
export function listLiveEvents(link: ControlMachineLink, id: string) { return controlRequest<{ items: LiveEvent[] }>(link, `/live-conversations/${id}/events?limit=100`); }
