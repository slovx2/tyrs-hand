import { ApiError, ClientApi } from "@/api/client";
import { getSyncCursor, setSyncCursor } from "@/db/cache";
import { clearServerSnapshot } from "@/db/database";
import { getToken, type Connection } from "@/db/connections";

export type SyncEvent = {
  kind: "durable" | "live";
  cursor?: number;
  sessionId: string | null;
  type: string;
  entityId: string;
  runEventSeq?: number;
  payload: unknown;
};

type Listener = (event: SyncEvent) => void;
const listeners = new Set<Listener>();

export function subscribeToUpdates(listener: Listener): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

function publish(event: SyncEvent): void {
  for (const listener of listeners) listener(event);
}

export class Synchronizer {
  private socket: WebSocket | null = null;
  private stopped = false;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;

  constructor(
    private readonly connection: Connection,
    private readonly onReset: () => Promise<void>,
  ) {}

  async start(): Promise<void> {
    this.stopped = false;
    await this.catchUp();
    await this.connect();
  }

  stop(): void {
    this.stopped = true;
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
    this.socket?.close();
    this.socket = null;
  }

  private async catchUp(): Promise<void> {
    const api = new ClientApi(this.connection);
    let cursor = await getSyncCursor(this.connection.serverId);
    try {
      for (;;) {
        const page = await api.sync(cursor);
        for (const update of page.updates) {
          publish({ ...update, sessionId: update.sessionId ?? null });
          cursor = update.cursor;
        }
        await setSyncCursor(this.connection.serverId, page.nextCursor);
        cursor = page.nextCursor;
        if (!page.hasMore) break;
      }
    } catch (error) {
      if (!(error instanceof ApiError) || !error.resetRequired) throw error;
      await clearServerSnapshot(this.connection.serverId);
      await this.onReset();
      cursor = await getSyncCursor(this.connection.serverId);
      const page = await api.sync(cursor);
      for (const update of page.updates) publish({ ...update, sessionId: update.sessionId ?? null });
      await setSyncCursor(this.connection.serverId, page.nextCursor);
    }
  }

  private async connect(): Promise<void> {
    if (this.stopped) return;
    const token = await getToken(this.connection.serverId);
    if (!token) throw new Error("设备凭证不存在");
    const cursor = await getSyncCursor(this.connection.serverId);
    const base = this.connection.baseUrl.replace(/^http/, "ws");
    this.socket = new WebSocket(`${base}/api/v1/client/updates?cursor=${cursor}`,
      [`tyrs-hand.bearer.${token}`]);
    this.socket.onmessage = (message) => {
      try {
        const notification = JSON.parse(String(message.data)) as { method: string; params: SyncEvent };
        if (notification.method !== "update") return;
        publish(notification.params);
        if (notification.params.kind === "durable" && notification.params.cursor !== undefined) {
          void setSyncCursor(this.connection.serverId, notification.params.cursor);
        }
      } catch { /* 无效事件由下一次 HTTP 补拉纠正。 */ }
    };
    this.socket.onclose = () => this.scheduleReconnect();
    this.socket.onerror = () => this.socket?.close();
  }

  private scheduleReconnect(): void {
    if (this.stopped) return;
    this.reconnectTimer = setTimeout(() => {
      void this.start().catch(() => this.scheduleReconnect());
    }, 1500);
  }
}
