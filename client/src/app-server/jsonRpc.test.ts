import { describe, expect, it, vi } from "vitest";

import { CodexJsonRpcClient, type AppServerSocket,
  type SocketCloseEvent, type SocketMessageEvent } from "./jsonRpc";

class FakeSocket implements AppServerSocket {
  readyState = 0;
  sent: Record<string, unknown>[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((event: SocketMessageEvent) => void) | null = null;
  onerror: (() => void) | null = null;
  onclose: ((event: SocketCloseEvent) => void) | null = null;

  send(data: string): void {
    if (this.readyState !== 1) throw new Error("socket closed");
    this.sent.push(JSON.parse(data) as Record<string, unknown>);
  }

  close(code = 1000, reason = ""): void {
    if (this.readyState === 3) return;
    this.readyState = 3;
    this.onclose?.({ code, reason });
  }

  open(): void {
    this.readyState = 1;
    this.onopen?.();
  }

  receive(message: Record<string, unknown>): void {
    this.onmessage?.({ data: JSON.stringify(message) });
  }

  fail(reason: string): void {
    this.readyState = 3;
    this.onerror?.();
    this.onclose?.({ code: 1006, reason });
  }
}

async function initialize(): Promise<{ client: CodexJsonRpcClient; socket: FakeSocket }> {
  const socket = new FakeSocket();
  const client = new CodexJsonRpcClient(() => socket, 1_000);
  const opening = client.open();
  await Promise.resolve();
  socket.open();
  await vi.waitFor(() => expect(socket.sent).toHaveLength(1));
  expect(socket.sent[0]).toMatchObject({ method: "initialize", params: {
    clientInfo: { name: "tyrs_hand_mobile" },
    capabilities: { experimentalApi: true },
  } });
  socket.receive({ id: socket.sent[0]!.id, result: {
    userAgent: "codex/0.147.0", codexHome: "/tmp/codex", platformFamily: "unix", platformOs: "linux",
  } });
  await opening;
  expect(socket.sent[1]).toEqual({ method: "initialized" });
  return { client, socket };
}

describe("CodexJsonRpcClient", () => {
  it("每条连接先 initialize，再发 initialized", async () => {
    const { client, socket } = await initialize();
    expect(client.isOpen()).toBe(true);
    expect(socket.sent.map((message) => message.method)).toEqual(["initialize", "initialized"]);
  });

  it("通知与响应反序时仍按 id 完成请求", async () => {
    const { client, socket } = await initialize();
    const methods: string[] = [];
    client.onNotification((notification) => methods.push(notification.method));
    const response = client.request<{ thread: { id: string } }>("thread/read",
      { threadId: "thread-1", includeTurns: true });
    const request = socket.sent.at(-1)!;

    socket.receive({ method: "turn/completed", params: {
      threadId: "thread-1", turn: { id: "turn-1", items: [], itemsView: "full",
        status: "completed", error: null, startedAt: 1, completedAt: 2, durationMs: 1_000 },
    } });
    socket.receive({ id: request.id, result: { thread: { id: "thread-1" } } });

    await expect(response).resolves.toEqual({ thread: { id: "thread-1" } });
    expect(methods).toEqual(["turn/completed"]);
  });

  it("并发响应乱序不会串请求", async () => {
    const { client, socket } = await initialize();
    const first = client.request<{ value: string }>("thread/read", { threadId: "first" });
    const second = client.request<{ value: string }>("thread/read", { threadId: "second" });
    const firstRequest = socket.sent.at(-2)!;
    const secondRequest = socket.sent.at(-1)!;

    socket.receive({ id: secondRequest.id, result: { value: "second" } });
    socket.receive({ id: firstRequest.id, result: { value: "first" } });

    await expect(first).resolves.toEqual({ value: "first" });
    await expect(second).resolves.toEqual({ value: "second" });
  });

  it("保留 Server Request 的原始 id 并直接回应", async () => {
    const { client, socket } = await initialize();
    client.onServerRequest((request) => client.respond(request.id, { answers: {} }));
    socket.receive({ id: "request-7", method: "item/tool/requestUserInput", params: {
      threadId: "thread-1", turnId: "turn-1", itemId: "item-1", questions: [], isBlocking: true,
      autoResolutionMs: null,
    } });
    expect(socket.sent.at(-1)).toEqual({ id: "request-7", result: { answers: {} } });
  });

  it("断线时将已发送但未响应的请求标记为结果未知", async () => {
    const { client, socket } = await initialize();
    const pending = client.request("turn/start", { threadId: "thread-1", input: [] });
    socket.close(1006, "network lost");
    await expect(pending).rejects.toMatchObject({ delivery: "unknown" });
  });

  it("连接失败时保留 close 原因并脱敏 loopback 路径", async () => {
    const socket = new FakeSocket();
    const client = new CodexJsonRpcClient(() => socket, 1_000);
    const opening = client.open();
    await Promise.resolve();

    socket.fail("failed ws://127.0.0.1:43210/random-secret: HTTP 502");

    await expect(opening).rejects.toThrow(
      "failed ws://127.0.0.1:43210/<redacted> HTTP 502",
    );
  });
});
