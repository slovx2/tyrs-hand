import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const state = vi.hoisted(() => ({
  calls: [] as string[],
  cursor: 0,
  responses: [] as (Record<string, unknown> | Error)[],
  cleared: [] as string[],
}));

vi.mock("@/api/client", async (importOriginal) => {
  const original = await importOriginal<typeof import("@/api/client")>();
  return { ...original, ClientApi: class {
    async sync() {
      state.calls.push("http");
      const response = state.responses.shift();
      if (response instanceof Error) throw response;
      return response;
    }
  } };
});
vi.mock("@/db/cache", () => ({
  getSyncCursor: async () => state.cursor,
  setSyncCursor: async (_serverId: string, cursor: number) => { state.cursor = cursor; },
}));
vi.mock("@/db/database", () => ({
  clearServerSnapshot: async (serverId: string) => { state.cleared.push(serverId); state.cursor = 0; },
}));
vi.mock("@/db/connections", () => ({ getToken: async () => "device-token" }));

// Mock 定义必须先于被测模块加载。
// eslint-disable-next-line import/first
import { ApiError } from "@/api/client";
// eslint-disable-next-line import/first
import { subscribeToUpdates, Synchronizer } from "./synchronizer";

class SocketStub {
  onmessage: ((event: { data: string }) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  constructor(readonly url: string) { state.calls.push("websocket"); }
  close() {}
}

const connection = { serverId: "control-a", baseUrl: "https://control.example",
  name: "Control A", deviceId: "device-a", active: true };

describe("Synchronizer", () => {
  beforeEach(() => {
    state.calls.length = 0;
    state.responses.length = 0;
    state.cleared.length = 0;
    state.cursor = 0;
    vi.stubGlobal("WebSocket", SocketStub);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("先补拉所有持久事件，再从最终游标打开 WebSocket", async () => {
    state.responses.push(
      { updates: [{ kind: "durable", cursor: 1, sessionId: null, type: "project.updated",
        entityId: "project-a", payload: {} }], nextCursor: 1, hasMore: true },
      { updates: [{ kind: "durable", cursor: 2, sessionId: null, type: "session.updated",
        entityId: "session-a", payload: {} }], nextCursor: 2, hasMore: false },
    );
    const received: number[] = [];
    const unsubscribe = subscribeToUpdates((event) => { if (event.cursor) received.push(event.cursor); });
    const synchronizer = new Synchronizer(connection, async () => undefined);
    await synchronizer.start();
    expect(state.calls).toEqual(["http", "http", "websocket"]);
    expect(state.cursor).toBe(2);
    expect(received).toEqual([1, 2]);
    synchronizer.stop();
    unsubscribe();
  });

  it("游标过期时只清理当前 Control 并从 bootstrap 水位恢复", async () => {
    state.cursor = 99;
    state.responses.push(new ApiError("reset", 410, true),
      { updates: [], nextCursor: 7, hasMore: false });
    const onReset = vi.fn(async () => { state.cursor = 6; });
    const synchronizer = new Synchronizer(connection, onReset);
    await synchronizer.start();
    expect(state.cleared).toEqual(["control-a"]);
    expect(onReset).toHaveBeenCalledOnce();
    expect(state.cursor).toBe(7);
    expect(state.calls).toEqual(["http", "http", "websocket"]);
    synchronizer.stop();
  });
});
