import type { ServerRequest } from "@codex-app-server/ServerRequest";
import type { JsonValue } from "@codex-app-server/serde_json/JsonValue";
import { describe, expect, it } from "vitest";

import { mcpToolApproval } from "@/app-server/mcpToolApproval";

import { approvalActions, approvalDescription, mcpToolApprovalActions,
  mcpToolApprovalDescription } from "./approvalActions";

type CommandRequest = Extract<ServerRequest, { method: "item/commandExecution/requestApproval" }>;
function command(params: Partial<CommandRequest["params"]> = {}): CommandRequest {
  return { id: "approval-1", method: "item/commandExecution/requestApproval", params: { kind: "command",
    threadId: "thread", turnId: "turn", itemId: "item", startedAtMs: 1, environmentId: null,
    command: "git status", ...params,
  } };
}

describe("手机原生审批选择", () => {
  it("终端输入与启动命令分别展示，并保留输入、原因及原生决策", () => {
    const request = command({ kind: "writeStdin", command: "write_stdin 123 'printf ok\n'",
      cwd: "/tmp/project", reason: "终端仍持有上一回合权限", availableDecisions: ["accept", "cancel"] });
    expect(approvalDescription(request)).toEqual({ title: "终端输入审批",
      detail: request.params.command, location: "/tmp/project", reason: request.params.reason });
    expect(approvalDescription(command()).title).toBe("命令审批");
    expect(approvalDescription(command({ kind: "writeStdin", command: null })).detail)
      .toBe("AI 请求向正在运行的终端发送输入");
    expect(approvalActions(request).map((action) => action.decision)).toEqual(["accept", "cancel"]);
  });

  it("保留原生允许的顺序，不展示未授权的允许按钮", () => {
    const actions = approvalActions(command({ availableDecisions: ["cancel", "decline"] }));
    expect(actions.map((action) => action.decision)).toEqual(["cancel", "decline"]);
    expect(actions.map((action) => action.title)).toEqual(["取消回合", "拒绝"]);
    expect(approvalActions(command({ availableDecisions: [] }))).toEqual([]);
  });

  it("允许本次、会话允许、拒绝、取消均返回明确原生决策", () => {
    expect(approvalActions(command()).map((action) => action.decision))
      .toEqual(["decline", "cancel", "accept", "acceptForSession"]);
    expect(approvalActions({ id: 1, method: "item/fileChange/requestApproval",
      params: { threadId: "thread", turnId: "turn", itemId: "item", startedAtMs: 1 } })
      .map((action) => action.decision)).toContain("cancel");
  });

  it("命令规则必须是原生提案，且展示完整命令前缀", () => {
    const accepted = { acceptWithExecpolicyAmendment: { execpolicy_amendment: ["git", "status"] } };
    const expanded = { acceptWithExecpolicyAmendment: { execpolicy_amendment: ["git"] } };
    const actions = approvalActions(command({ proposedExecpolicyAmendment: ["git", "status"],
      availableDecisions: [accepted, expanded] }));
    expect(actions).toHaveLength(1);
    expect(actions[0]).toMatchObject({ decision: accepted, detail: '今后允许此命令前缀：["git","status"]' });
  });

  it("网络规则不能扩大主机或隐藏新增字段", () => {
    const allowed = { applyNetworkPolicyAmendment: {
      network_policy_amendment: { host: "localhost", action: "allow" as const },
    } };
    const expanded = { applyNetworkPolicyAmendment: {
      network_policy_amendment: { host: "*", action: "allow" as const },
    } };
    const hidden = { applyNetworkPolicyAmendment: {
      network_policy_amendment: { host: "localhost", action: "allow" as const, allowAll: true },
    } };
    expect(approvalActions(command({
      proposedNetworkPolicyAmendments: [{ action: "allow", host: "localhost" }],
      availableDecisions: [allowed, expanded, hidden],
    }))).toEqual([{ id: "network-policy-0", title: "允许并记住此主机",
      detail: "网络规则：localhost", decision: allowed }]);
  });
});

describe("手机 MCP 工具审批", () => {
  it("展示服务、问话，并按引擎排好的参数逐项列出", () => {
    expect(mcpToolApprovalDescription(toolApproval({ persist: "session",
      tool_params: { path: "/tmp/a.txt", options: { depth: 2 }, recursive: true },
      tool_params_display: [
        { name: "path", value: "/tmp/a.txt", display_name: "路径" },
        { name: "options", value: { depth: 2 }, display_name: "options" },
        { name: "recursive", value: true, display_name: "recursive" },
      ] }))).toEqual({ title: "MCP 工具审批", detail: "允许 MCP 服务 fixture 运行工具“echo”吗？",
      serverName: "fixture",
      params: ["路径：/tmp/a.txt", 'options：\n{\n  "depth": 2\n}', "recursive：true"] });
  });

  it("没有展示参数时展示完整参数 JSON，没有参数和问话时使用默认说明", () => {
    expect(mcpToolApprovalDescription(toolApproval({ tool_params: { path: "/tmp/a.txt" } })).params)
      .toEqual(['参数：\n{\n  "path": "/tmp/a.txt"\n}']);
    expect(mcpToolApprovalDescription(toolApproval({ tool_params: {} })).params).toEqual(["参数：{}"]);
    expect(mcpToolApprovalDescription(toolApproval({}, ""))).toMatchObject({
      detail: "AI 请求调用 MCP 工具", params: [] });
  });

  it("沿用命令审批的按钮文案，只展示引擎声明的记住范围", () => {
    const titles = (persist?: JsonValue) => mcpToolApprovalActions(toolApproval(
      persist === undefined ? {} : { persist })).map((action) => action.title);
    expect(titles()).toEqual(["允许本次", "拒绝", "取消回合"]);
    expect(titles("session")).toEqual(["允许本次", "允许本会话", "拒绝", "取消回合"]);
    expect(titles(["session", "always"]))
      .toEqual(["允许本次", "允许本会话", "允许并记住此工具", "拒绝", "取消回合"]);
  });

  it("每个按钮返回对应的征询应答", () => {
    expect(mcpToolApprovalActions(toolApproval({ persist: ["session", "always"] }))).toEqual([
      { id: "accept", title: "允许本次", answer: { action: "accept", content: {}, _meta: null } },
      { id: "acceptForSession", title: "允许本会话",
        answer: { action: "accept", content: {}, _meta: { persist: "session" } } },
      { id: "acceptAlways", title: "允许并记住此工具", detail: "今后调用此工具不再询问",
        answer: { action: "accept", content: {}, _meta: { persist: "always" } } },
      { id: "decline", title: "拒绝", answer: { action: "decline", content: null, _meta: null } },
      { id: "cancel", title: "取消回合", answer: { action: "cancel", content: null, _meta: null } },
    ]);
  });
});

function toolApproval(meta: { [key in string]?: JsonValue },
  message = "允许 MCP 服务 fixture 运行工具“echo”吗？") {
  return mcpToolApproval({ id: "approval-1", method: "mcpServer/elicitation/request", params: {
    threadId: "thread", turnId: "turn", serverName: "fixture", mode: "form", message,
    _meta: { codex_approval_kind: "mcp_tool_call", ...meta },
    requestedSchema: { type: "object", properties: {} },
  } })!;
}
