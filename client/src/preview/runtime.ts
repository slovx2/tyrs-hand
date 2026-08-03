import type { Connection } from "@/db/connections";
import type { LocalAttachment, OutboxItem } from "@/sync/outbox";
import type { Message, Session, SessionSettings } from "@/types/protocol";
import { isPreviewMode } from "./config";
import { createPreviewSeed, type PreviewControlSeed } from "./fixtures";

let state = createPreviewSeed();
let idCounter = 1;

function nextId(): string {
  const suffix = String(idCounter++).padStart(12, "0");
  return `90000000-0000-4000-8000-${suffix}`;
}

function control(serverId: string): PreviewControlSeed {
  const value = state.controls[serverId];
  if (!value) throw new Error("预览 Control 不存在");
  return value;
}

function now(): string {
  return new Date().toISOString();
}

function parseBody(init?: RequestInit): Record<string, unknown> {
  if (typeof init?.body !== "string") return {};
  return JSON.parse(init.body) as Record<string, unknown>;
}

function findSession(serverId: string, sessionId: string): Session {
  const item = control(serverId).sessions.find((session) => session.id === sessionId);
  if (!item) throw new Error("预览会话不存在");
  return item;
}

function updateSession(serverId: string, sessionId: string, update: Partial<Session>): Session {
  const item = findSession(serverId, sessionId);
  Object.assign(item, update, { updatedAt: now() });
  return structuredClone(item);
}

function createMessage(sessionId: string, role: "user" | "agent", text: string,
  localId = nextId()): Message {
  const details = Object.values(state.controls).flatMap((item) => Object.values(item.details));
  const seq = (details.find((item) => item.messages.some((message) => message.sessionId === sessionId))
    ?.messages.length ?? 0) + 1;
  const timestamp = now();
  return {
    id: nextId(), sessionId, seq, localId, participantId: null, role,
    content: { type: "text", text }, attachments: [], createdAt: timestamp, updatedAt: timestamp,
  };
}

function createSession(serverId: string, body: Record<string, unknown>): Session {
  const value = control(serverId);
  const initial = body.initialMessage as { localId?: string; text?: string } | undefined;
  const incoming = body.settings as SessionSettings;
  const timestamp = now();
  const item: Session = {
    id: nextId(),
    developmentEnvironmentId: value.bootstrap.projects.find((project) => project.id === body.projectId)
      ?.environmentId ?? value.bootstrap.projects[0]!.environmentId,
    developmentProjectId: String(body.projectId ?? value.bootstrap.projects[0]!.id),
    agentProfileId: incoming.agentProfileId,
    title: String(initial?.text ?? "新的预览任务").slice(0, 28),
    lifecycleState: "active", historyCompleteness: "complete", model: incoming.model,
    reasoningEffort: incoming.reasoningEffort, serviceTier: incoming.serviceTier,
    collaborationMode: incoming.collaborationMode, settingsVersion: incoming.settingsVersion + 1,
    lastMessageSeq: 1, lastActivityAt: timestamp, createdAt: timestamp, updatedAt: timestamp,
  };
  value.sessions.unshift(item);
  value.details[item.id] = {
    settings: { ...incoming, settingsVersion: item.settingsVersion },
    currentRun: null,
    messages: [createMessage(item.id, "user", String(initial?.text ?? ""), initial?.localId)],
  };
  value.bootstrap.lastStartedSettings = { ...incoming, settingsVersion: item.settingsVersion };
  return structuredClone(item);
}

function sendMessage(serverId: string, sessionId: string, body: Record<string, unknown>): Message {
  const value = control(serverId);
  const detail = value.details[sessionId];
  if (!detail) throw new Error("预览会话详情不存在");
  const item = createMessage(sessionId, "user", String(body.text ?? ""), String(body.localId ?? ""));
  detail.messages.push(item);
  updateSession(serverId, sessionId, { lastMessageSeq: item.seq, lastActivityAt: item.createdAt });
  return structuredClone(item);
}

function queueLiveUpdate(sessionId: string): void {
  if (!isPreviewMode) return;
  setTimeout(() => void import("@/sync/synchronizer").then(({ publishLocalUpdate }) => publishLocalUpdate({
    kind: "live", sessionId, type: "item/agentMessage/delta", entityId: sessionId,
    runEventSeq: 5, payload: { itemId: "commentary-1-2", phase: "commentary",
      delta: "\n\n正在生成预览响应…" },
  })), 80);
}

export function listPreviewConnections(): Connection[] {
  return structuredClone(state.connections);
}

export function setPreviewActiveConnection(serverId: string): void {
  state.connections.forEach((item) => { item.active = item.serverId === serverId; });
}

export function renamePreviewConnection(serverId: string, name: string): void {
  const item = state.connections.find((connection) => connection.serverId === serverId);
  if (item) item.name = name.trim();
}

export function removePreviewConnection(serverId: string): void {
  state.connections = state.connections.filter((item) => item.serverId !== serverId);
  if (state.connections.length > 0 && !state.connections.some((item) => item.active)) {
    state.connections[0]!.active = true;
  }
}

export function previewBootstrap(serverId: string) {
  return structuredClone(control(serverId).bootstrap);
}

export function previewSessions(serverId: string) {
  return structuredClone(control(serverId).sessions);
}

export function previewMessages(serverId: string, sessionId: string) {
  return structuredClone(control(serverId).details[sessionId]?.messages ?? []);
}

export async function requestPreview(serverId: string, path: string, init?: RequestInit): Promise<unknown> {
  const method = init?.method ?? "GET";
  const url = new URL(path, "https://preview.tyrshand.local");
  const body = parseBody(init);
  const value = control(serverId);
  if (url.pathname === "/bootstrap") return previewBootstrap(serverId);
  if (url.pathname === "/sessions" && method === "GET") {
    const lifecycle = url.searchParams.get("lifecycle");
    const projectId = url.searchParams.get("projectId");
    const sessions = value.sessions.filter((item) => (!lifecycle || item.lifecycleState === lifecycle)
      && (!projectId || item.developmentProjectId === projectId));
    return { sessions: structuredClone(sessions), nextCursor: "" };
  }
  if (url.pathname === "/sessions" && method === "POST") {
    return { session: createSession(serverId, body), deduplicated: false };
  }
  if (url.pathname === "/uploads" && method === "POST") {
    return { attachment: { id: nextId(), sessionId: null, kind: "file", filename: "预览附件",
      mediaType: "application/octet-stream", sizeBytes: 1024, sha256: "preview-upload", status: "uploaded",
      createdAt: now() }, deduplicated: false };
  }
  const messageMatch = /^\/sessions\/([^/]+)\/messages$/.exec(url.pathname);
  if (messageMatch && method === "GET") {
    const all = value.details[messageMatch[1]!]?.messages ?? [];
    const before = Number(url.searchParams.get("beforeSeq") ?? Number.POSITIVE_INFINITY);
    const after = Number(url.searchParams.get("afterSeq") ?? 0);
    const limit = Number(url.searchParams.get("limit") ?? 100);
    const filtered = all.filter((item) => item.seq < before && item.seq > after);
    const messages = filtered.slice(-limit);
    return { messages: structuredClone(messages), lastMessageSeq: all.at(-1)?.seq ?? 0,
      hasMoreBefore: filtered.length > messages.length, hasMoreAfter: false };
  }
  if (messageMatch && method === "POST") {
    const item = sendMessage(serverId, messageMatch[1]!, body);
    return { message: item, intentId: nextId(), deduplicated: false };
  }
  const sessionMatch = /^\/sessions\/([^/]+)$/.exec(url.pathname);
  if (sessionMatch && method === "GET") {
    const sessionId = sessionMatch[1]!;
    const detail = value.details[sessionId];
    if (!detail) throw new Error("预览会话详情不存在");
    if (detail.currentRun?.status === "running") queueLiveUpdate(sessionId);
    return { session: structuredClone(findSession(serverId, sessionId)),
      settings: structuredClone(detail.settings), currentRun: structuredClone(detail.currentRun) };
  }
  if (sessionMatch && method === "PATCH") {
    const sessionId = sessionMatch[1]!;
    const current = findSession(serverId, sessionId);
    const detail = value.details[sessionId]!;
    const nextVersion = current.settingsVersion + 1;
    const allowed = ["agentProfileId", "model", "reasoningEffort", "serviceTier", "collaborationMode"] as const;
    for (const key of allowed) if (body[key] !== undefined) {
      Object.assign(current, { [key]: body[key] });
      Object.assign(detail.settings, { [key]: body[key] });
    }
    if (typeof body.title === "string") current.title = body.title;
    current.settingsVersion = nextVersion;
    detail.settings.settingsVersion = nextVersion;
    return updateSession(serverId, sessionId, {});
  }
  const actionMatch = /^\/sessions\/([^/]+)\/(stop|archive|restore)$/.exec(url.pathname);
  if (actionMatch && method === "POST") {
    const sessionId = actionMatch[1]!;
    const action = actionMatch[2];
    if (action === "stop" && value.details[sessionId]?.currentRun) {
      value.details[sessionId]!.currentRun!.status = "canceled";
      value.details[sessionId]!.currentRun!.finishedAt = now();
    } else updateSession(serverId, sessionId,
      { lifecycleState: action === "archive" ? "archived" : "active" });
    return {};
  }
  const planMatch = /^\/sessions\/([^/]+)\/plans\/[^/]+\/execute$/.exec(url.pathname);
  if (planMatch && method === "POST") {
    const detail = value.details[planMatch[1]!]!;
    if (detail.currentRun) {
      detail.currentRun.status = "running";
      detail.currentRun.actualSettings.collaborationMode = "default";
      detail.currentRun.finishedAt = null;
    }
    return {};
  }
  const answerMatch = /^\/interactive\/([^/]+)\/answer$/.exec(url.pathname);
  if (answerMatch && method === "POST") {
    for (const detail of Object.values(value.details)) if (detail.currentRun) {
      detail.currentRun.pendingInteractives = detail.currentRun.pendingInteractives
        .filter((item) => item.id !== answerMatch[1]);
      if (detail.currentRun.pendingInteractives.length === 0 && detail.currentRun.status === "waiting_for_user") {
        detail.currentRun.status = "running";
      }
    }
    return {};
  }
  if (url.pathname === "/sync") return { updates: [], nextCursor: value.bootstrap.currentCursor,
    hasMore: false, latestCursor: value.bootstrap.currentCursor };
  if (url.pathname === "/device" || url.pathname === "/device/push-token") return {};
  throw new Error(`预览 API 尚未实现：${method} ${url.pathname}`);
}

export function listPreviewOutbox(serverId: string, sessionId?: string): OutboxItem[] {
  return structuredClone(control(serverId).outbox.filter((item) => !sessionId || item.sessionId === sessionId));
}

export function enqueuePreviewTask(input: { connection: Connection; localId: string; projectId: string;
  text: string; settings: SessionSettings; attachments: LocalAttachment[] }): void {
  control(input.connection.serverId).outbox.push({
    serverId: input.connection.serverId, localId: input.localId, kind: "create_session",
    sessionId: null, projectId: input.projectId, status: "pending", error: null,
    payload: { text: input.text, settings: input.settings, attachments: input.attachments },
  });
}

export function enqueuePreviewMessage(input: { connection: Connection; localId: string; sessionId: string;
  text: string; attachments: LocalAttachment[] }): void {
  control(input.connection.serverId).outbox.push({
    serverId: input.connection.serverId, localId: input.localId, kind: "send_message",
    sessionId: input.sessionId, projectId: null, status: "pending", error: null,
    payload: { text: input.text, attachments: input.attachments },
  });
}

export function retryPreviewOutbox(serverId: string, localId: string): void {
  const item = control(serverId).outbox.find((candidate) => candidate.localId === localId);
  if (item) { item.status = "pending"; item.error = null; }
}

export function discardPreviewOutbox(serverId: string, localId: string): void {
  const value = control(serverId);
  value.outbox = value.outbox.filter((item) => item.localId !== localId);
}

export function recoverPreviewOutbox(serverId: string): void {
  for (const item of control(serverId).outbox) if (item.status === "uploading" || item.status === "sending") {
    item.status = "pending";
  }
}

export function processPreviewOutbox(connection: Connection): void {
  const value = control(connection.serverId);
  const completed: string[] = [];
  for (const item of value.outbox) {
    if (item.status !== "pending") continue;
    item.status = "sending";
    if (item.kind === "create_session" && item.projectId && item.payload.settings) {
      createSession(connection.serverId, { projectId: item.projectId, settings: item.payload.settings,
        initialMessage: { localId: item.localId, text: item.payload.text, attachmentIds: [] } });
      completed.push(item.localId);
    } else if (item.kind === "send_message" && item.sessionId) {
      sendMessage(connection.serverId, item.sessionId,
        { localId: item.localId, text: item.payload.text, attachmentIds: [] });
      completed.push(item.localId);
    } else {
      item.status = "failed";
      item.error = "预览 Outbox 数据不完整";
    }
  }
  value.outbox = value.outbox.filter((item) => !completed.includes(item.localId));
}

export function resetPreviewState(): void {
  state = createPreviewSeed();
  idCounter = 1;
}
