import type { RequestId } from "@codex-app-server/RequestId";
import type { ServerNotification } from "@codex-app-server/ServerNotification";
import type { ServerRequest } from "@codex-app-server/ServerRequest";
import type { JsonValue } from "@codex-app-server/serde_json/JsonValue";
import { describe, expect, it } from "vitest";

import { mcpToolApproval, mcpToolApprovalResponse } from "./mcpToolApproval";
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

function toolApprovalRequest(id: RequestId, meta: { [key in string]?: JsonValue } = {
  codex_approval_kind: "mcp_tool_call", persist: ["session", "always"], tool_params: { text: "hi" },
}): ServerRequest {
  return { id, method: "mcpServer/elicitation/request", params: {
    threadId: "thread-1", turnId: "turn-1", serverName: "fixture", mode: "form", _meta: meta,
    message: "允许 MCP 服务 fixture 运行工具“echo”吗？",
    requestedSchema: { type: "object", properties: {} },
  } };
}

describe.each(["codex", "claude-code"] as const)("%s MCP 工具审批", (engine) => {
  it("允许本次、记住、拒绝、取消按原生征询应答发回", () => {
    const rpc = new InteractionRpc();
    const client = new OfficialAppServerClient("profile", engine, rpc, journal);
    const decisions = mcpToolApproval(toolApprovalRequest("probe"))!.decisions;
    decisions.forEach((decision, id) => {
      const request = toolApprovalRequest(id);
      rpc.requestListener!(request);
      expect(client.answerRequest(request, mcpToolApprovalResponse(decision))).toBe(true);
    });
    expect(client.pendingRequests()).toEqual([]);
    expect(rpc.responses).toEqual([
      { id: 0, result: { action: "accept", content: {}, _meta: null } },
      { id: 1, result: { action: "accept", content: {}, _meta: { persist: "session" } } },
      { id: 2, result: { action: "accept", content: {}, _meta: { persist: "always" } } },
      { id: 3, result: { action: "decline", content: null, _meta: null } },
      { id: 4, result: { action: "cancel", content: null, _meta: null } },
    ]);
  });

  it("发送新消息前撤销时只拒绝本次工具调用，其他征询仍取消", async () => {
    const rpc = new InteractionRpc();
    const client = new OfficialAppServerClient("profile", engine, rpc, journal);
    rpc.requestListener!(toolApprovalRequest("tool"));
    rpc.requestListener!(toolApprovalRequest("auth", { codex_approval_kind: "browser_auth" }));
    rpc.requestListener!(toolApprovalRequest("form", {}));
    rpc.requestListener!(nativeRequest("mcp", "url"));
    await client.dismissPendingRequests("thread-1");
    expect(client.pendingRequests()).toEqual([]);
    expect(rpc.responses).toEqual([
      { id: "tool", result: { action: "decline", content: null, _meta: null } },
      { id: "auth", result: { action: "cancel", content: null, _meta: null } },
      { id: "form", result: { action: "cancel", content: null, _meta: null } },
      { id: "url", result: { action: "cancel", content: null, _meta: null } },
    ]);
  });
});
