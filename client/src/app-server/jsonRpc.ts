import type { InitializeParams } from "@codex-app-server/InitializeParams";
import type { InitializeResponse } from "@codex-app-server/InitializeResponse";
import type { RequestId } from "@codex-app-server/RequestId";
import type { ServerNotification } from "@codex-app-server/ServerNotification";
import type { ServerRequest } from "@codex-app-server/ServerRequest";

export const CODEX_APP_SERVER_VERSION = "0.147.0";

export type SocketMessageEvent = { data: unknown };
export type SocketCloseEvent = { code?: number; reason?: string };

export interface AppServerSocket {
  readonly readyState: number;
  send(data: string): void;
  close(code?: number, reason?: string): void;
  onopen: (() => void) | null;
  onmessage: ((event: SocketMessageEvent) => void) | null;
  onerror: (() => void) | null;
  onclose: ((event: SocketCloseEvent) => void) | null;
}

export type SocketFactory = () => Promise<AppServerSocket> | AppServerSocket;
export type RequestDelivery = "not_sent" | "unknown" | "rejected";

export class JsonRpcRequestError extends Error {
  constructor(
    message: string,
    readonly method: string,
    readonly delivery: RequestDelivery,
    readonly code?: number,
    readonly data?: unknown,
  ) {
    super(message);
  }
}

type RpcError = { code: number; message: string; data?: unknown };
type RpcResponse = { id: RequestId; result?: unknown; error?: RpcError };
type PendingRequest = {
  method: string;
  resolve: (value: unknown) => void;
  reject: (reason: JsonRpcRequestError) => void;
  timer: ReturnType<typeof setTimeout>;
};

type NotificationListener = (notification: ServerNotification) => void;
type ServerRequestListener = (request: ServerRequest) => void;
type CloseListener = (error: Error) => void;

const SOCKET_OPEN = 1;

function redactLoopbackPath(message: string): string {
  return message.replace(
    /((?:ws|http):\/\/(?:127\.0\.0\.1|localhost):\d+\/)[^\s'"),]+/gi,
    "$1<redacted>",
  );
}

export class CodexJsonRpcClient {
  private socket: AppServerSocket | null = null;
  private opening: Promise<InitializeResponse> | null = null;
  private initializeResponse: InitializeResponse | null = null;
  private nextId = 0;
  private readonly pending = new Map<string, PendingRequest>();
  private readonly notifications = new Set<NotificationListener>();
  private readonly serverRequests = new Set<ServerRequestListener>();
  private readonly closeListeners = new Set<CloseListener>();

  constructor(
    private readonly socketFactory: SocketFactory,
    private readonly requestTimeoutMs = 30_000,
  ) {}

  open(): Promise<InitializeResponse> {
    if (this.initializeResponse) return Promise.resolve(this.initializeResponse);
    if (this.opening) return this.opening;
    this.opening = this.openConnection().finally(() => { this.opening = null; });
    return this.opening;
  }

  isOpen(): boolean {
    return this.socket?.readyState === SOCKET_OPEN && this.initializeResponse !== null;
  }

  async request<Result>(method: string, params?: unknown): Promise<Result> {
    return this.sendRequest(method, params, false);
  }

  private async sendRequest<Result>(method: string, params: unknown, beforeInitialized: boolean): Promise<Result> {
    if (!beforeInitialized && !this.isOpen()) {
      throw new JsonRpcRequestError("尚未初始化 Codex App Server 连接", method, "not_sent");
    }
    const socket = this.socket;
    if (!socket || socket.readyState !== SOCKET_OPEN) {
      throw new JsonRpcRequestError("Codex App Server 连接未打开", method, "not_sent");
    }
    const id = ++this.nextId;
    return new Promise<Result>((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(String(id));
        reject(new JsonRpcRequestError(`${method} 响应超时`, method, "unknown"));
      }, this.requestTimeoutMs);
      this.pending.set(String(id), {
        method,
        resolve: (value) => resolve(value as Result),
        reject,
        timer,
      });
      try {
        socket.send(JSON.stringify(params === undefined ? { id, method } : { id, method, params }));
      } catch (error) {
        clearTimeout(timer);
        this.pending.delete(String(id));
        reject(new JsonRpcRequestError(
          error instanceof Error ? error.message : `${method} 发送失败`,
          method,
          "unknown",
        ));
      }
    });
  }

  notify(method: string, params?: unknown): void {
    const socket = this.socket;
    if (!socket || socket.readyState !== SOCKET_OPEN) {
      throw new JsonRpcRequestError("Codex App Server 连接未打开", method, "not_sent");
    }
    socket.send(JSON.stringify(params === undefined ? { method } : { method, params }));
  }

  respond(id: RequestId, result: unknown): void {
    this.writeResponse({ id, result });
  }

  respondError(id: RequestId, code: number, message: string, data?: unknown): void {
    this.writeResponse({ id, error: data === undefined ? { code, message } : { code, message, data } });
  }

  onNotification(listener: NotificationListener): () => void {
    this.notifications.add(listener);
    return () => this.notifications.delete(listener);
  }

  onServerRequest(listener: ServerRequestListener): () => void {
    this.serverRequests.add(listener);
    return () => this.serverRequests.delete(listener);
  }

  onClose(listener: CloseListener): () => void {
    this.closeListeners.add(listener);
    return () => this.closeListeners.delete(listener);
  }

  close(): void {
    this.socket?.close(1000, "client closed");
    this.fail(new Error("Codex App Server 连接已关闭"));
  }

  private async openConnection(): Promise<InitializeResponse> {
    const socket = await this.socketFactory();
    this.socket = socket;
    await this.waitUntilOpen(socket);
    const params: InitializeParams = {
      clientInfo: { name: "tyrs_hand_mobile", title: "Tyrs Hand Mobile", version: "0.1.0" },
      capabilities: {
        experimentalApi: true,
        requestAttestation: false,
        extensions: null,
        optOutNotificationMethods: null,
      },
    };
    const response = await this.sendRequest<InitializeResponse>("initialize", params, true);
    this.initializeResponse = response;
    this.notify("initialized");
    return response;
  }

  private waitUntilOpen(socket: AppServerSocket): Promise<void> {
    return new Promise((resolve, reject) => {
      let settled = false;
      let errorFallback: ReturnType<typeof setTimeout> | null = null;
      const finish = (operation: () => void) => {
        if (settled) return;
        settled = true;
        if (errorFallback !== null) clearTimeout(errorFallback);
        operation();
      };
      socket.onmessage = (event) => this.handleMessage(event.data);
      // React Native 会先发 error，再同步发出携带底层原因的 close。延迟一个
      // event-loop tick，避免通用错误覆盖更有诊断价值的 close.reason。
      socket.onerror = () => {
        if (errorFallback !== null) return;
        errorFallback = setTimeout(() => finish(() => reject(
          new Error("Codex App Server WebSocket 连接失败"),
        )), 0);
      };
      socket.onclose = (event) => {
        const reason = redactLoopbackPath(event.reason ?? "");
        const error = new Error(reason || `Codex App Server WebSocket 已断开 (${event.code ?? 0})`);
        if (!settled) finish(() => reject(error));
        this.fail(error);
      };
      if (socket.readyState === SOCKET_OPEN) finish(resolve);
      else socket.onopen = () => finish(resolve);
    });
  }

  private handleMessage(raw: unknown): void {
    if (typeof raw !== "string") return;
    let message: Record<string, unknown>;
    try {
      message = JSON.parse(raw) as Record<string, unknown>;
    } catch {
      this.fail(new Error("Codex App Server 返回了非法 JSON"));
      return;
    }
    if ((typeof message.id === "string" || typeof message.id === "number") &&
      typeof message.method === "string") {
      for (const listener of this.serverRequests) listener(message as ServerRequest);
      return;
    }
    if (typeof message.id === "string" || typeof message.id === "number") {
      this.resolveResponse(message as RpcResponse);
      return;
    }
    if (typeof message.method === "string") {
      for (const listener of this.notifications) listener(message as ServerNotification);
    }
  }

  private resolveResponse(response: RpcResponse): void {
    const pending = this.pending.get(String(response.id));
    if (!pending) return;
    clearTimeout(pending.timer);
    this.pending.delete(String(response.id));
    if (response.error) {
      pending.reject(new JsonRpcRequestError(response.error.message, pending.method, "rejected",
        response.error.code, response.error.data));
    } else {
      pending.resolve(response.result);
    }
  }

  private writeResponse(response: RpcResponse): void {
    const socket = this.socket;
    if (!socket || socket.readyState !== SOCKET_OPEN) {
      throw new Error("Codex App Server 连接已断开");
    }
    socket.send(JSON.stringify(response));
  }

  private fail(error: Error): void {
    if (!this.socket && !this.initializeResponse) return;
    this.socket = null;
    this.initializeResponse = null;
    for (const request of this.pending.values()) {
      clearTimeout(request.timer);
      request.reject(new JsonRpcRequestError(error.message, request.method, "unknown"));
    }
    this.pending.clear();
    for (const listener of this.closeListeners) listener(error);
  }
}
