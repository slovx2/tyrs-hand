import type { ServerRequest } from "@codex-app-server/ServerRequest";
import type { CommandExecutionApprovalDecision } from "@codex-app-server/v2/CommandExecutionApprovalDecision";

type ApprovalRequest = Extract<ServerRequest, { method:
  "item/commandExecution/requestApproval" | "item/fileChange/requestApproval" }>;
export type ApprovalAction = {
  id: string; title: string; detail?: string;
  decision: CommandExecutionApprovalDecision;
};

const titles = {
  accept: "允许本次", acceptForSession: "允许本会话", decline: "拒绝", cancel: "取消回合",
} as const;

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
