import type { ServerRequest } from "@codex-app-server/ServerRequest";
import type { Thread } from "@codex-app-server/v2/Thread";
import type { Turn } from "@codex-app-server/v2/Turn";
import type { UserInput } from "@codex-app-server/v2/UserInput";

import type { AppServerSocket, SocketMessageEvent } from "@/app-server/jsonRpc";
import type { Connection } from "@/db/connections";
import { createPreviewSeed, type PreviewControlSeed } from "./fixtures";

let state = createPreviewSeed();
let idCounter = 1;

function control(serverId: string): PreviewControlSeed {
  const value = state.controls[serverId];
  if (!value) throw new Error("预览 Control 不存在");
  return value;
}

function nextId(): string {
  return `90000000-0000-4000-8000-${String(idCounter++).padStart(12, "0")}`;
}

export function listPreviewConnections(): Connection[] {
  return structuredClone(state.connections);
}

export function setPreviewActiveConnection(profileId: string): void {
  state.connections.forEach((item) => { item.active = item.profileId === profileId; });
}

export function renamePreviewConnection(profileId: string, name: string): void {
  const item = state.connections.find((connection) => connection.profileId === profileId);
  if (item) item.name = name.trim();
}

export function removePreviewConnection(profileId: string): void {
  state.connections = state.connections.filter((item) => item.profileId !== profileId);
  if (state.connections.length > 0 && !state.connections.some((item) => item.active)) {
    state.connections[0]!.active = true;
  }
}

export async function requestPreview(serverId: string, path: string,
  init?: RequestInit): Promise<unknown> {
  const method = init?.method ?? "GET";
  if (path === "/bootstrap") return structuredClone(control(serverId).bootstrap);
  if (path === "/tunnels" && method === "POST") return { tunnelId: nextId(),
    expiresAt: new Date(Date.now() + 45_000).toISOString(), websocketPath: "/preview/app-server" };
  if (path === "/materializations" && method === "POST") return { attachment: {
    id: nextId(), sha256: "preview", filename: "预览附件", mediaType: "application/octet-stream",
    sizeBytes: 1024, remotePath: "/preview/cache/preview-attachment",
    inputType: "mention" }, deduplicated: false };
  if (path === "/device" || path === "/device/push-token") return {};
  throw new Error(`预览 Control API 尚未实现：${method} ${path}`);
}

export function createPreviewAppServerSocket(serverId: string): AppServerSocket {
  return new PreviewSocket(serverId);
}

class PreviewSocket implements AppServerSocket {
  readyState = 0;
  onopen: (() => void) | null = null;
  onmessage: ((event: SocketMessageEvent) => void) | null = null;
  onerror: (() => void) | null = null;
  onclose: ((event: { code?: number; reason?: string }) => void) | null = null;
  private readonly outstandingRequests = new Map<string, ServerRequest>();

  constructor(private readonly serverId: string) {
    setTimeout(() => { this.readyState = 1; this.onopen?.(); }, 0);
  }

  send(raw: string): void {
    const message = JSON.parse(raw) as Record<string, unknown>;
    if (typeof message.method === "string" && (typeof message.id === "number" ||
      typeof message.id === "string")) {
      void this.handleRequest(message.id, message.method, message.params).catch((error) =>
        this.emit({ id: message.id, error: { code: -32603,
          message: error instanceof Error ? error.message : "预览请求失败" } }));
      return;
    }
    if ((typeof message.id === "number" || typeof message.id === "string") &&
      ("result" in message || "error" in message)) {
      const request = this.outstandingRequests.get(String(message.id));
      if (!request) return;
      this.outstandingRequests.delete(String(message.id));
      control(this.serverId).requests = control(this.serverId).requests.filter((item) =>
        String(item.id) !== String(message.id));
      this.emit({ method: "serverRequest/resolved", params: {
        threadId: requestThreadId(request), requestId: request.id,
      } });
      const threadId = requestThreadId(request);
      if (threadId) this.completeActiveTurn(threadId, "已收到回答，继续执行。", 30);
    }
  }

  close(code = 1000, reason = "closed"): void {
    if (this.readyState === 3) return;
    this.readyState = 3;
    this.onclose?.({ code, reason });
  }

  private async handleRequest(id: string | number, method: string, rawParams: unknown): Promise<void> {
    const params = (rawParams ?? {}) as Record<string, unknown>;
    const value = control(this.serverId);
    if (method === "initialize") { this.emit({ id, result: { userAgent: "preview/0.147.0",
      platformFamily: "unix", platformOs: "preview" } }); return; }
    if (method === "thread/list") {
      const archived = params.archived === true;
      const cwd = typeof params.cwd === "string" ? params.cwd : null;
      const data = value.threads.filter((thread) => value.archivedThreadIds.includes(thread.id) === archived &&
        (!cwd || thread.cwd === cwd)).map((thread) => ({ ...thread, turns: [] }));
      this.emit({ id, result: { data, nextCursor: null } }); return;
    }
    if (method === "thread/read" || method === "thread/resume") {
      const thread = requireThread(value, String(params.threadId));
      this.emit({ id, result: { thread: structuredClone(thread) } });
      if (method === "thread/resume") setTimeout(() => this.emitPending(thread.id), 0);
      return;
    }
    if (method === "thread/start") {
      const thread = newThread(String(params.cwd ?? "/preview"));
      value.threads.unshift(thread);
      this.emit({ id, result: { thread: structuredClone(thread) } });
      this.emit({ method: "thread/started", params: { thread: { ...thread, turns: [] } } });
      return;
    }
    if (method === "model/list") {
      this.emit({ id, result: { data: structuredClone(state.models), nextCursor: null } }); return;
    }
    if (method === "turn/start") {
      const thread = requireThread(value, String(params.threadId));
      const turn = startTurn(thread, params);
      this.emit({ id, result: { turn: structuredClone(turn) } });
      this.emit({ method: "turn/started", params: { threadId: thread.id, turn: structuredClone(turn) } });
      this.completeActiveTurn(thread.id, "预览任务已按官方协议完成。", 80);
      return;
    }
    if (method === "turn/steer") {
      const thread = requireThread(value, String(params.threadId));
      const active = [...thread.turns].reverse().find((turn) => turn.status === "inProgress");
      if (!active) { this.emit({ id, error: { code: -32600, message: "no active turn to steer" } }); return; }
      if (params.expectedTurnId !== active.id) { this.emit({ id, error: { code: -32600,
        message: `expected active turn id ${String(params.expectedTurnId)} but found ${active.id}` } }); return; }
      active.items.push(userItem(params));
      this.emit({ id, result: { turnId: active.id } });
      this.completeActiveTurn(thread.id, "已接收 steer 消息。", 80);
      return;
    }
    if (method === "turn/interrupt") {
      const thread = requireThread(value, String(params.threadId));
      const turn = thread.turns.find((item) => item.id === params.turnId);
      if (turn) { turn.status = "interrupted"; turn.completedAt = Math.floor(Date.now() / 1000); }
      thread.status = { type: "idle" };
      this.emit({ id, result: {} });
      if (turn) this.emit({ method: "turn/completed", params: { threadId: thread.id,
        turn: structuredClone(turn) } });
      return;
    }
    if (method === "thread/archive" || method === "thread/unarchive") {
      const thread = requireThread(value, String(params.threadId));
      const archived = method === "thread/archive";
      value.archivedThreadIds = archived
        ? [...new Set([...value.archivedThreadIds, thread.id])]
        : value.archivedThreadIds.filter((item) => item !== thread.id);
      this.emit({ id, result: archived ? {} : { thread: structuredClone(thread) } });
      this.emit({ method: archived ? "thread/archived" : "thread/unarchived",
        params: archived ? { threadId: thread.id } : { thread: structuredClone(thread) } });
      return;
    }
    if (method === "thread/name/set") {
      const thread = requireThread(value, String(params.threadId));
      thread.name = String(params.name); thread.updatedAt = Math.floor(Date.now() / 1000);
      this.emit({ id, result: {} });
      this.emit({ method: "thread/name/updated", params: { threadId: thread.id, name: thread.name } });
      return;
    }
    this.emit({ id, error: { code: -32601, message: `预览方法未实现：${method}` } });
  }

  private emitPending(threadId: string): void {
    for (const request of control(this.serverId).requests.filter((item) =>
      requestThreadId(item) === threadId)) {
      this.outstandingRequests.set(String(request.id), request);
      this.emit(structuredClone(request));
    }
  }

  private completeActiveTurn(threadId: string, text: string, delay: number): void {
    setTimeout(() => {
      const thread = requireThread(control(this.serverId), threadId);
      const turn = [...thread.turns].reverse().find((item) => item.status === "inProgress");
      if (!turn) return;
      turn.items.push({ type: "agentMessage", id: nextId(), text,
        phase: "final_answer", memoryCitation: null });
      turn.status = "completed"; turn.completedAt = Math.floor(Date.now() / 1000);
      thread.status = { type: "idle" }; thread.updatedAt = turn.completedAt;
      this.emit({ method: "turn/completed", params: { threadId, turn: structuredClone(turn) } });
    }, delay);
  }

  private emit(message: unknown): void {
    if (this.readyState === 3) return;
    this.onmessage?.({ data: JSON.stringify(message) });
  }
}

function requireThread(value: PreviewControlSeed, id: string): Thread {
  const thread = value.threads.find((item) => item.id === id);
  if (!thread) throw new Error("预览 Thread 不存在");
  return thread;
}

function requestThreadId(request: ServerRequest): string | null {
  const params = request.params as { threadId?: unknown };
  return typeof params.threadId === "string" ? params.threadId : null;
}

function newThread(cwd: string): Thread {
  const now = Math.floor(Date.now() / 1000);
  const id = nextId();
  return { id, extra: null, sessionId: id, forkedFromId: null, parentThreadId: null,
    preview: "新的预览任务", ephemeral: false, section: null, sectionEnteredAt: null,
    historyMode: "legacy", modelProvider: "openai", createdAt: now, updatedAt: now,
    recencyAt: now, status: { type: "idle" }, path: null, cwd, cliVersion: "0.147.0",
    source: "appServer", canAcceptDirectInput: true, threadSource: null, agentNickname: null,
    agentRole: null, gitInfo: null, name: null, turns: [] };
}

function startTurn(thread: Thread, params: Record<string, unknown>): Turn {
  const now = Math.floor(Date.now() / 1000);
  const turn: Turn = { id: nextId(), status: "inProgress", items: [userItem(params)],
    itemsView: "full", error: null, startedAt: now, completedAt: null, durationMs: null };
  thread.turns.push(turn); thread.status = { type: "active", activeFlags: [] };
  return turn;
}

function userItem(params: Record<string, unknown>): Turn["items"][number] {
  return { type: "userMessage", id: nextId(),
    clientId: typeof params.clientUserMessageId === "string" ? params.clientUserMessageId : null,
    content: Array.isArray(params.input) ? params.input as UserInput[] : [] };
}

export function resetPreviewState(): void {
  state = createPreviewSeed(); idCounter = 1;
}
