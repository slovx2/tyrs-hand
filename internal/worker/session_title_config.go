package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Codex 不识别 Claude 的 default_tools_enabled。标题会话使用原生开关和空环境，
// 逐项关闭继承的 MCP；仅传空对象会被深合并，留下宿主已有服务。
func codexSessionTitleConfig(ctx context.Context, client sessionTitleCaller, cwd string) (map[string]any, error) {
	var response struct {
		Config *struct {
			MCPServers map[string]json.RawMessage `json:"mcp_servers"`
		} `json:"config"`
	}
	if err := client.Call(ctx, "config/read", map[string]any{"cwd": cwd, "includeLayers": false}, &response); err != nil {
		return nil, fmt.Errorf("读取标题会话工具边界失败: %w", err)
	}
	if response.Config == nil {
		return nil, errors.New("读取标题会话工具边界失败: 缺少原生配置")
	}
	servers := make(map[string]any, len(response.Config.MCPServers))
	for name := range response.Config.MCPServers {
		servers[name] = map[string]any{"enabled": false}
	}
	return map[string]any{
		"model_reasoning_effort": "low", "service_tier": "fast",
		"web_search": "disabled", "agents": map[string]any{"enabled": false},
		"mcp_servers": servers,
		"tools": map[string]any{
			"update_plan":                     map[string]any{"enabled": false},
			"experimental_request_user_input": map[string]any{"enabled": false},
		},
		"features": map[string]any{
			"memories": false, "shell_tool": false, "multi_agent": false, "multi_agent_v2": false,
			"apps": false, "plugins": false, "code_mode": false, "code_mode_only": false,
			"view_image": false, "image_generation": false, "request_permissions_tool": false,
			"token_budget": false, "current_time_reminder": false, "sleep_tool": false,
			"deferred_executor": false, "tool_suggest": false, "send_message_to_user_async": false,
			"goals": false,
		},
	}, nil
}
