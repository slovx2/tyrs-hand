import type { ServerRequest } from "@codex-app-server/ServerRequest";
import type { JsonValue } from "@codex-app-server/serde_json/JsonValue";
import type { McpServerElicitationRequestResponse } from "@codex-app-server/v2/McpServerElicitationRequestResponse";

// 引擎用 MCP 征询询问“是否允许调用这个 MCP 工具”：表单为空，靠 _meta 里的标记识别。
export type McpToolApprovalDecision = "accept" | "acceptForSession" | "acceptAlways" | "decline" | "cancel";
export type McpToolApprovalParam = { name: string; displayName: string; value: JsonValue };
export type McpToolApproval = {
  serverName: string; message: string;
  // 只包含引擎声明可用的选择，顺序即展示顺序。
  decisions: McpToolApprovalDecision[];
  // 引擎排好的展示参数；没有或无法识别时为 null，改用 params。
  displayParams: McpToolApprovalParam[] | null;
  params: JsonValue | null;
};

function object(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown> : null;
}

function displayParams(value: unknown): McpToolApprovalParam[] | null {
  if (!Array.isArray(value) || value.length === 0) return null;
  const params: McpToolApprovalParam[] = [];
  for (const raw of value) {
    const entry = object(raw);
    // 任何一项无法识别都整体放弃，避免只展示部分参数。
    if (!entry || typeof entry.name !== "string" || !Object.hasOwn(entry, "value")) return null;
    params.push({ name: entry.name, value: entry.value as JsonValue,
      displayName: typeof entry.display_name === "string" && entry.display_name
        ? entry.display_name : entry.name });
  }
  return params;
}

// 其他带 codex_approval_kind 的征询（登录、工具推荐）不是工具审批，返回 null。
export function mcpToolApproval(request: ServerRequest): McpToolApproval | null {
  if (request.method !== "mcpServer/elicitation/request" || request.params.mode !== "form") return null;
  const meta = object(request.params._meta);
  if (meta?.codex_approval_kind !== "mcp_tool_call") return null;
  // persist 可能是单个字符串，也可能是数组。
  const persist: unknown[] = Array.isArray(meta.persist) ? meta.persist : [meta.persist];
  return {
    serverName: request.params.serverName, message: request.params.message,
    decisions: ["accept",
      ...(persist.includes("session") ? ["acceptForSession" as const] : []),
      ...(persist.includes("always") ? ["acceptAlways" as const] : []),
      "decline", "cancel"],
    displayParams: displayParams(meta.tool_params_display),
    params: (meta.tool_params ?? null) as JsonValue | null,
  };
}

// 是否记住这次选择通过 _meta.persist 返回；拒绝和取消没有内容。
export function mcpToolApprovalResponse(decision: McpToolApprovalDecision): McpServerElicitationRequestResponse {
  switch (decision) {
  case "accept": return { action: "accept", content: {}, _meta: null };
  case "acceptForSession": return { action: "accept", content: {}, _meta: { persist: "session" } };
  case "acceptAlways": return { action: "accept", content: {}, _meta: { persist: "always" } };
  case "decline": return { action: "decline", content: null, _meta: null };
  case "cancel": return { action: "cancel", content: null, _meta: null };
  }
}
