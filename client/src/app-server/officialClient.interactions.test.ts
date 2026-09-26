import type { RequestId } from "@codex-app-server/RequestId";
import type { ServerNotification } from "@codex-app-server/ServerNotification";
import type { ServerRequest } from "@codex-app-server/ServerRequest";
import { describe, expect, it } from "vitest";

import { OfficialAppServerClient, type OfficialRpcClient } from "./officialClient";
import type { SubmissionJournal } from "./submissions";

class InteractionRpc implements OfficialRpcClient {
  responses: { id: RequestId; result: unknown }[] = [];
  notification: ((notification: ServerNotification) => void) | undefined;
  requestListener: ((request: ServerRequest) => void) | undefined;
  closed: ((error: Error) => void) | undefined;
  async open(): Promise<unknown> { return {}; }
  async request<Result>(): Promise<Result> { throw new Error("本测试不应调用客户端请求"); }
  respond(id: RequestId, result: unknown): void { this.responses.push({ id, result }); }
  respondError(): void { throw new Error("本测试不应产生原生错误响应"); }
  onNotification(listener: (notification: ServerNotification) => void): () => void {
    this.notification = listener; return () => { this.notification = undefined; };
  }
  onServerRequest(listener: (request: ServerRequest) => void): () => void {
    this.requestListener = listener; return () => { this.requestListener = undefined; };
  }
  onClose(listener: (error: Error) => void): () => void {
    this.closed = listener; return () => { this.closed = undefined; };
  }
}

const journal: SubmissionJournal = {
  prepare: async () => undefined, setThread: async () => undefined,
  markUnknown: async () => undefined, complete: async () => undefined,
};
type Kind = "permissions" | "mcp";
function nativeRequest(kind: Kind, id: RequestId, threadId = "thread-1"): ServerRequest {
  return kind === "permissions"
    ? { id, method: "item/permissions/requestApproval", params: {
      threadId, turnId: "turn-1", itemId: "item-1", environmentId: null, startedAtMs: 1,
      cwd: "/workspace", reason: null, permissions: { network: { enabled: true }, fileSystem: null },
    } }
    : { id, method: "mcpServer/elicitation/request", params: {
      threadId, turnId: null, serverName: "fixture", mode: "url", _meta: null,
      message: "确认", url: "http://localhost/fixture", elicitationId: "fixture",
    } };
}
function denial(kind: Kind): unknown {
  return kind === "permissions" ? { permissions: {}, scope: "turn" }
    : { action: "cancel", content: null, _meta: null };
}

describe.each(["codex", "claude-code"] as const)("%s 原生交互身份", (engine) => {
  it.each(["permissions", "mcp"] as const)("%s 数字和字符串 ID 不合并", (kind) => {
    const rpc = new InteractionRpc();
    const client = new OfficialAppServerClient("profile", engine, rpc, journal);
    const numeric = nativeRequest(kind, 9);
    const text = nativeRequest(kind, "9");
    rpc.requestListener!(numeric);
    rpc.requestListener!(text);
    expect(client.pendingRequests()).toEqual([numeric, text]);
    expect(client.answerRequest(numeric, denial(kind))).toBe(true);
    expect(client.pendingRequests()).toEqual([text]);
    expect(client.answerRequest(text, denial(kind))).toBe(true);
    expect(rpc.responses.map((response) => response.id)).toEqual([9, "9"]);
  });

  it.each(["permissions", "mcp"] as const)("%s resolved 必须匹配线程及 ID 类型", (kind) => {
    const rpc = new InteractionRpc();
    const client = new OfficialAppServerClient("profile", engine, rpc, journal);
    const request = nativeRequest(kind, 9);
    rpc.requestListener!(request);
    rpc.notification!({ method: "serverRequest/resolved", params: { threadId: "other", requestId: 9 } });
    rpc.notification!({ method: "serverRequest/resolved", params: { threadId: "thread-1", requestId: "9" } });
    expect(client.pendingRequests()).toEqual([request]);
    rpc.notification!({ method: "serverRequest/resolved", params: { threadId: "thread-1", requestId: 9 } });
    expect(client.pendingRequests()).toEqual([]);
    expect(client.answerRequest(request, denial(kind))).toBe(false);
    expect(rpc.responses).toEqual([]);
  });

  it.each(["permissions", "mcp"] as const)("%s 已决议同 ID 重用不能接收旧卡片回答", (kind) => {
    const rpc = new InteractionRpc();
    const client = new OfficialAppServerClient("profile", engine, rpc, journal);
    const oldRequest = nativeRequest(kind, "reused");
    rpc.requestListener!(oldRequest);
    rpc.notification!({ method: "serverRequest/resolved", params: { threadId: "thread-1", requestId: "reused" } });
    const currentRequest = nativeRequest(kind, "reused");
    rpc.requestListener!(currentRequest);
    expect(client.answerRequest(oldRequest, denial(kind))).toBe(false);
    expect(client.pendingRequests()[0]).toBe(currentRequest);
    expect(rpc.responses).toEqual([]);
    expect(client.answerRequest(currentRequest, denial(kind))).toBe(true);
  });

  it.each(["permissions", "mcp"] as const)("%s 重连后同 ID 请求不能接收旧连接回答", (kind) => {
    const rpc = new InteractionRpc();
    const client = new OfficialAppServerClient("profile", engine, rpc, journal);
    const oldRequest = nativeRequest(kind, "reused");
    rpc.requestListener!(oldRequest);
    rpc.closed!(new Error("SSH 已断线"));
    const currentRequest = nativeRequest(kind, "reused");
    rpc.requestListener!(currentRequest);
    expect(client.answerRequest(oldRequest, denial(kind))).toBe(false);
    expect(client.pendingRequests()[0]).toBe(currentRequest);
    expect(rpc.responses).toEqual([]);
    expect(client.answerRequest(currentRequest, denial(kind))).toBe(true);
  });
});
