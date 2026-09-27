import type { ServerRequest } from "@codex-app-server/ServerRequest";
import { describe, expect, it } from "vitest";

import { approvalActions, approvalDescription } from "./approvalActions";

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
