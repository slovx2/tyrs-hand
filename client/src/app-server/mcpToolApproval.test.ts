import type { ServerRequest } from "@codex-app-server/ServerRequest";
import type { JsonValue } from "@codex-app-server/serde_json/JsonValue";
import { describe, expect, it } from "vitest";

import { mcpToolApproval, mcpToolApprovalResponse } from "./mcpToolApproval";

type McpRequest = Extract<ServerRequest, { method: "mcpServer/elicitation/request" }>;
function form(meta: JsonValue | null): McpRequest {
  return { id: "approval-1", method: "mcpServer/elicitation/request", params: {
    threadId: "thread", turnId: "turn", serverName: "fixture", mode: "form", _meta: meta,
    message: "允许 MCP 服务 fixture 运行工具“echo”吗？",
    requestedSchema: { type: "object", properties: {} },
  } };
}
const toolCall = { codex_approval_kind: "mcp_tool_call" };

describe("MCP 工具审批识别", () => {
  it("带工具调用标记的空表单征询识别为工具审批", () => {
    const params = { path: "/tmp/a.txt", recursive: true };
    const display = [
      { name: "path", value: "/tmp/a.txt", display_name: "路径" },
      { name: "recursive", value: true, display_name: "recursive" },
    ];
    expect(mcpToolApproval(form({ ...toolCall, persist: "session", tool_params: params,
      tool_params_display: display }))).toEqual({
      serverName: "fixture", message: "允许 MCP 服务 fixture 运行工具“echo”吗？",
      decisions: ["accept", "acceptForSession", "decline", "cancel"],
      displayParams: [{ name: "path", value: "/tmp/a.txt", displayName: "路径" },
        { name: "recursive", value: true, displayName: "recursive" }],
      params,
    });
  });

  it("persist 是字符串或数组时只提供引擎声明的记住范围", () => {
    const decisions = (persist?: JsonValue) => mcpToolApproval(form(persist === undefined
      ? toolCall : { ...toolCall, persist }))!.decisions;
    expect(decisions()).toEqual(["accept", "decline", "cancel"]);
    expect(decisions("session")).toEqual(["accept", "acceptForSession", "decline", "cancel"]);
    expect(decisions("always")).toEqual(["accept", "acceptAlways", "decline", "cancel"]);
    expect(decisions(["session"])).toEqual(["accept", "acceptForSession", "decline", "cancel"]);
    expect(decisions(["always", "session"]))
      .toEqual(["accept", "acceptForSession", "acceptAlways", "decline", "cancel"]);
    expect(decisions("forever")).toEqual(["accept", "decline", "cancel"]);
    expect(decisions([])).toEqual(["accept", "decline", "cancel"]);
  });

  it("展示参数缺失或无法识别时保留完整参数", () => {
    const params = { path: "/tmp/a.txt" };
    const approval = (display?: JsonValue) => mcpToolApproval(form({ ...toolCall, tool_params: params,
      ...(display === undefined ? {} : { tool_params_display: display }) }))!;
    expect(approval()).toMatchObject({ displayParams: null, params });
    expect(approval([])).toMatchObject({ displayParams: null, params });
    expect(approval("path")).toMatchObject({ displayParams: null, params });
    expect(approval([{ name: "path", value: "/tmp/a.txt" }, { value: 1 }]))
      .toMatchObject({ displayParams: null, params });
    expect(approval([{ name: "path", value: "/tmp/a.txt" }]).displayParams)
      .toEqual([{ name: "path", value: "/tmp/a.txt", displayName: "path" }]);
    expect(mcpToolApproval(form(toolCall))).toMatchObject({ displayParams: null, params: null });
  });

  it("不是工具调用的征询不识别为工具审批", () => {
    expect(mcpToolApproval(form(null))).toBeNull();
    expect(mcpToolApproval(form({ persist: "session" }))).toBeNull();
    expect(mcpToolApproval(form({ codex_approval_kind: "browser_auth", persist: "session" }))).toBeNull();
    expect(mcpToolApproval(form({ codex_approval_kind: "tool_suggestion" }))).toBeNull();
    expect(mcpToolApproval(form([toolCall]))).toBeNull();
    const base = { threadId: "thread", turnId: "turn", serverName: "fixture", _meta: toolCall,
      message: "确认" };
    expect(mcpToolApproval({ ...form(toolCall), params: { ...base, mode: "url",
      url: "http://localhost/fixture", elicitationId: "fixture" } })).toBeNull();
    expect(mcpToolApproval({ ...form(toolCall), params: { ...base, mode: "openai/form",
      requestedSchema: { type: "object", properties: {} } } })).toBeNull();
    expect(mcpToolApproval({ id: 1, method: "item/fileChange/requestApproval",
      params: { threadId: "thread", turnId: "turn", itemId: "item", startedAtMs: 1 } })).toBeNull();
  });

  it("允许本次、记住、拒绝、取消分别返回明确的征询应答", () => {
    expect(mcpToolApprovalResponse("accept")).toEqual({ action: "accept", content: {}, _meta: null });
    expect(mcpToolApprovalResponse("acceptForSession"))
      .toEqual({ action: "accept", content: {}, _meta: { persist: "session" } });
    expect(mcpToolApprovalResponse("acceptAlways"))
      .toEqual({ action: "accept", content: {}, _meta: { persist: "always" } });
    expect(mcpToolApprovalResponse("decline")).toEqual({ action: "decline", content: null, _meta: null });
    expect(mcpToolApprovalResponse("cancel")).toEqual({ action: "cancel", content: null, _meta: null });
  });
});
