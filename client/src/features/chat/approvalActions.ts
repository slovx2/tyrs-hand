import type { ServerRequest } from "@codex-app-server/ServerRequest";
import type { JsonValue } from "@codex-app-server/serde_json/JsonValue";
import type { CommandExecutionApprovalDecision } from "@codex-app-server/v2/CommandExecutionApprovalDecision";
import type { McpServerElicitationRequestResponse } from "@codex-app-server/v2/McpServerElicitationRequestResponse";

import { mcpToolApprovalResponse, type McpToolApproval,
  type McpToolApprovalDecision } from "@/app-server/mcpToolApproval";

type ApprovalRequest = Extract<ServerRequest, { method:
  "item/commandExecution/requestApproval" | "item/fileChange/requestApproval" }>;
export type ApprovalAction = {
  id: string; title: string; detail?: string;
  decision: CommandExecutionApprovalDecision;
};

export type McpToolApprovalAction = {
  id: McpToolApprovalDecision; title: string; detail?: string;
  answer: McpServerElicitationRequestResponse;
};

const titles = {
  accept: "允许本次", acceptForSession: "允许本会话", decline: "拒绝", cancel: "取消回合",
} as const;

function paramLine(label: string, value: JsonValue): string {
  const text = typeof value === "string" ? value : JSON.stringify(value, null, 2);
  return `${label}：${text.includes("\n") ? "\n" : ""}${text}`;
}

export function mcpToolApprovalDescription(approval: McpToolApproval) {
  return {
    title: "MCP 工具审批",
    detail: approval.message || "AI 请求调用 MCP 工具",
    serverName: approval.serverName,
    // 优先按引擎排好的参数逐项展示；没有时展示完整参数 JSON。
    params: approval.displayParams?.map((param) => paramLine(param.displayName, param.value)) ??
      (approval.params === null ? [] : [paramLine("参数", approval.params)]),
  };
}

export function mcpToolApprovalActions(approval: McpToolApproval): McpToolApprovalAction[] {
  return approval.decisions.map((decision): McpToolApprovalAction => decision === "acceptAlways"
    ? { id: decision, title: "允许并记住此工具", detail: "今后调用此工具不再询问",
      answer: mcpToolApprovalResponse(decision) }
    : { id: decision, title: titles[decision], answer: mcpToolApprovalResponse(decision) });
}

export function approvalDescription(request: ApprovalRequest) {
  if (request.method === "item/fileChange/requestApproval") {
    return { title: "文件修改审批", detail: request.params.reason ?? "AI 请求修改文件",
      location: request.params.grantRoot, reason: null };
  }
  const stdin = request.params.kind === "writeStdin";
  return {
    title: stdin ? "终端输入审批" : "命令审批",
    detail: request.params.command ?? (stdin ? "AI 请求向正在运行的终端发送输入" : "AI 请求执行命令"),
    location: request.params.cwd,
    reason: request.params.reason,
  };
}

// 没有决策列表时使用协议内的显式选择；空列表表示没有可用选择。
export function approvalActions(request: ApprovalRequest): ApprovalAction[] {
  const available = request.method === "item/commandExecution/requestApproval"
    ? request.params.availableDecisions : undefined;
  const decisions = available ?? ["decline", "cancel", "accept", "acceptForSession"];
  return decisions.flatMap((decision, index): ApprovalAction[] => {
    if (typeof decision === "string") {
      if (!Object.hasOwn(titles, decision)) return [];
      const key = decision as keyof typeof titles;
      return [{ id: key, title: titles[key], decision: key }];
    }
    if (!decision || request.method !== "item/commandExecution/requestApproval") return [];
    if ("acceptWithExecpolicyAmendment" in decision) {
      const proposal = request.params.proposedExecpolicyAmendment;
      if (!proposal?.length || JSON.stringify(decision) !== JSON.stringify({
        acceptWithExecpolicyAmendment: { execpolicy_amendment: proposal },
      })) return [];
      return [{ id: "exec-policy-" + index, title: "允许并记住命令规则",
        detail: "今后允许此命令前缀：" + JSON.stringify(proposal), decision }];
    }
    if ("applyNetworkPolicyAmendment" in decision) {
      const policy = decision.applyNetworkPolicyAmendment.network_policy_amendment;
      if (!policy || Object.keys(policy).length !== 2) return [];
      const proposal = request.params.proposedNetworkPolicyAmendments?.find((candidate) =>
        candidate.host === policy.host && candidate.action === policy.action);
      if (!proposal || JSON.stringify(decision) !== JSON.stringify({
        applyNetworkPolicyAmendment: { network_policy_amendment: policy },
      })) return [];
      return [{ id: "network-policy-" + index,
        title: policy.action === "allow" ? "允许并记住此主机" : "拒绝并记住此主机",
        detail: "网络规则：" + policy.host, decision }];
    }
    return [];
  });
}
