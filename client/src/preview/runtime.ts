import type { ServerRequest } from "@codex-app-server/ServerRequest";
import type { Thread } from "@codex-app-server/v2/Thread";
import type { Turn } from "@codex-app-server/v2/Turn";
import type { UserInput } from "@codex-app-server/v2/UserInput";

import type { AppServerSocket, SocketMessageEvent } from "@/app-server/jsonRpc";
import type { Connection } from "@/db/connections";
import { createPreviewSeed, previewSessionIds, type PreviewControlSeed } from "./fixtures";

let state = createPreviewSeed();
let idCounter = 1;
let stateGeneration = 0;
const activityTimelines = new Set<string>();
export const previewActivityTimelineMs = {
  toolCompleted: 8_000,
  finalStarted: 14_000,
  turnCompleted: 18_000,
} as const;

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
      const data = value.threads.filter((thread) => !thread.ephemeral &&
        value.archivedThreadIds.includes(thread.id) === archived &&
        (!cwd || thread.cwd === cwd)).map((thread) => ({ ...thread, turns: [] }));
      this.emit({ id, result: { data, nextCursor: null } }); return;
    }
    if (method === "thread/read") {
      const thread = requireThread(value, String(params.threadId));
      this.emit({ id, result: { thread: threadResult(thread, params.includeTurns === true) } });
      return;
    }
    if (method === "thread/resume") {
      const thread = requireThread(value, String(params.threadId));
      const pageParams = params.initialTurnsPage as Record<string, unknown> | null | undefined;
      this.emit({ id, result: {
        thread: threadResult(thread, params.excludeTurns !== true),
        initialTurnsPage: pageParams ? turnsPage(thread, pageParams) : null,
      } });
      setTimeout(() => this.emitPending(thread.id), 0);
      this.startActivityTimeline(thread.id);
      return;
    }
    if (method === "thread/turns/list") {
      const thread = requireThread(value, String(params.threadId));
      this.emit({ id, result: turnsPage(thread, params) });
      return;
    }
    if (method === "thread/items/list") {
      const thread = value.threads.find((item) => item.id === String(params.threadId));
      if (!thread) {
        this.emit({ id, error: { code: -32004, message: "预览会话不存在" } });
        return;
      }
      const turnId = typeof params.turnId === "string" ? params.turnId : null;
      const direction = params.sortDirection === "desc" ? "desc" : "asc";
      const limit = typeof params.limit === "number" && params.limit > 0
        ? Math.floor(params.limit) : 100;
      const match = typeof params.cursor === "string"
        ? params.cursor.match(/^preview-items:(asc|desc):(\d+)$/) : null;
      const offset = match?.[1] === direction ? Number(match[2]) : 0;
      const entries = thread.turns.flatMap((turn) => turnId && turn.id !== turnId ? []
        : turn.items.map((item) => ({ turnId: turn.id, item })));
      const ordered = direction === "desc" ? [...entries].reverse() : entries;
      const data = ordered.slice(offset, offset + limit);
      const nextOffset = offset + data.length;
      this.emit({ id, result: { data: structuredClone(data),
        nextCursor: nextOffset < ordered.length
          ? `preview-items:${direction}:${nextOffset}` : null,
        backwardsCursor: data.length > 0
          ? `preview-items:${direction === "desc" ? "asc" : "desc"}:0` : null } });
      return;
    }
    if (method === "thread/start") {
      const historyMode = params.historyMode === "paginated" ? "paginated" : "legacy";
      const thread = newThread(String(params.cwd ?? "/preview"), params.ephemeral === true,
        historyMode);
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
      const structuredTitle = params.outputSchema && typeof params.outputSchema === "object";
      this.completeActiveTurn(thread.id, structuredTitle
        ? JSON.stringify({ title: "生成预览任务标题", description: "预览任务自动标题" })
        : "预览任务已按官方协议完成。", 80);
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
      this.emit({ method: "thread/name/updated",
        params: { threadId: thread.id, threadName: thread.name } });
      return;
    }
    if (method === "thread/unsubscribe") {
      const threadId = String(params.threadId);
      const thread = value.threads.find((item) => item.id === threadId);
      if (thread?.ephemeral) value.threads = value.threads.filter((item) => item.id !== threadId);
      this.emit({ id, result: { status: "notLoaded" } });
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
      const item: Turn["items"][number] = { type: "agentMessage", id: nextId(), text,
        phase: "final_answer", memoryCitation: null };
      turn.items.push(item);
      turn.status = "completed"; turn.completedAt = Math.floor(Date.now() / 1000);
      thread.status = { type: "idle" }; thread.updatedAt = turn.completedAt;
      this.emit({ method: "item/completed", params: {
        threadId, turnId: turn.id, item: structuredClone(item),
      } });
      this.emit({ method: "turn/completed", params: { threadId, turn: structuredClone(turn) } });
    }, delay);
  }

  private startActivityTimeline(threadId: string): void {
    if (threadId !== previewSessionIds.running || activityTimelines.has(threadId)) return;
    activityTimelines.add(threadId);
    const generation = stateGeneration;
    const apply = (delay: number, update: (thread: Thread, turn: Turn) => unknown) => {
      setTimeout(() => {
        if (generation !== stateGeneration) return;
        const thread = requireThread(control(this.serverId), threadId);
        const turn = thread.turns.find((item) => item.id === "turn-running");
        if (!turn || turn.status !== "inProgress") return;
        const notifications = update(thread, turn);
        for (const notification of Array.isArray(notifications)
          ? notifications : [notifications]) {
          if (notification) this.emit(notification);
        }
      }, delay);
    };
    apply(previewActivityTimelineMs.toolCompleted, (_thread, turn) => {
      const commands = turn.items.filter((item) => item.type === "commandExecution");
      for (const command of commands) {
        command.status = "completed";
        command.exitCode = 0;
        command.durationMs = previewActivityTimelineMs.toolCompleted;
      }
      return commands.map((command) => ({ method: "item/completed",
        params: { threadId, turnId: turn.id, item: structuredClone(command) } }));
    });
    apply(previewActivityTimelineMs.finalStarted, (_thread, turn) => {
      turn.items.push({ type: "agentMessage", id: "agent-running-final",
        text: "最终回答开始后，处理过程应当已经自动收起。",
        phase: "final_answer", memoryCitation: null });
      return { method: "item/agentMessage/delta", params: { threadId, turnId: turn.id,
        itemId: "agent-running-final", delta: "最终回答开始" } };
    });
    apply(previewActivityTimelineMs.turnCompleted, (thread, turn) => {
      turn.status = "completed"; turn.completedAt = Math.floor(Date.now() / 1000);
      turn.durationMs = previewActivityTimelineMs.turnCompleted;
      thread.status = { type: "idle" }; thread.updatedAt = turn.completedAt;
      return { method: "turn/completed", params: { threadId, turn: structuredClone(turn) } };
    });
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

function threadResult(thread: Thread, includeTurns: boolean): Thread {
  return structuredClone(includeTurns ? thread : { ...thread, turns: [] });
}

function turnsPage(thread: Thread, params: Record<string, unknown>): {
  data: Turn[];
  nextCursor: string | null;
  backwardsCursor: string | null;
} {
  const direction = params.sortDirection === "asc" ? "asc" : "desc";
  const limit = typeof params.limit === "number" && params.limit > 0
    ? Math.floor(params.limit) : 20;
  const cursor = typeof params.cursor === "string" ? params.cursor : null;
  const match = cursor?.match(/^preview-turns:(asc|desc):(\d+)$/);
  const offset = match?.[1] === direction ? Number(match[2]) : 0;
  const ordered = direction === "desc" ? [...thread.turns].reverse() : [...thread.turns];
  const data = ordered.slice(offset, offset + limit);
  const nextOffset = offset + data.length;
  return {
    data: structuredClone(data),
    nextCursor: nextOffset < ordered.length
      ? `preview-turns:${direction}:${nextOffset}` : null,
    backwardsCursor: data.length > 0
      ? `preview-turns:${direction === "desc" ? "asc" : "desc"}:0` : null,
  };
}

function newThread(cwd: string, ephemeral = false,
  historyMode: Thread["historyMode"] = "legacy"): Thread {
  const now = Math.floor(Date.now() / 1000);
  const id = nextId();
  return { id, extra: null, sessionId: id, forkedFromId: null, parentThreadId: null,
    preview: "新的预览任务", ephemeral, section: null, sectionEnteredAt: null,
    historyMode, modelProvider: "openai", createdAt: now, updatedAt: now,
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
  stateGeneration += 1; activityTimelines.clear(); state = createPreviewSeed(); idCounter = 1;
}
