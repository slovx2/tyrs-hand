//go:build integration

package bootstrap

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

const controlClaudePermissionTool = "mcp__tyrs_permissions__request_permissions"

type claudePermissionResult struct {
	Type      string
	ToolUseID string `json:"tool_use_id"`
	IsError   bool   `json:"is_error"`
	Content   json.RawMessage
}

// 真实 SDK 执行权限 MCP 和 Write；Mock 只返回工具提案，不注入回调或副作用。
func (s *nativePermissionScenario) serveClaude(t *testing.T, w http.ResponseWriter, req *http.Request) {
	if strings.Contains(req.URL.Path, "count_tokens") {
		_, _ = io.WriteString(w, `{"input_tokens":10}`)
		return
	}
	if req.URL.Path != "/v1/messages" {
		http.NotFound(w, req)
		return
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, 8<<20))
	if !assert.NoError(t, err) {
		return
	}
	var payload struct {
		Messages []struct{ Content json.RawMessage }
		Tools    []struct{ Name string }
	}
	if !assert.NoError(t, json.Unmarshal(body, &payload)) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	active := s.active
	if active == nil || !strings.Contains(string(body), active.id) {
		bootstrapModelText(w, true)
		return
	}
	var permission, write *claudePermissionResult
	for _, message := range payload.Messages {
		var blocks []claudePermissionResult
		if json.Unmarshal(message.Content, &blocks) != nil {
			continue
		}
		for _, block := range blocks {
			if block.Type != "tool_result" {
				continue
			}
			switch block.ToolUseID {
			case active.id:
				permission = &block
			case active.id + "-write":
				write = &block
			}
		}
	}
	if write != nil {
		active.commandSeen = true
		assert.Equal(t, !active.allowed, write.IsError, "真实 Write 工具结果必须与授权范围一致")
		bootstrapModelText(w, true)
		return
	}
	if !active.executeOnly && permission == nil {
		active.permissionCalls++
		declared := false
		for _, tool := range payload.Tools {
			declared = declared || tool.Name == controlClaudePermissionTool
		}
		assert.True(t, declared, "权限工具必须由真实 SDK 向模型声明")
		claudePermissionModelTool(w, active.id, controlClaudePermissionTool, map[string]any{
			"permissions": map[string]any{"fileSystem": map[string]any{"write": []string{s.grantRoot}}},
			"reason":      active.id,
		})
		return
	}
	if permission != nil {
		active.permissionSeen = true
		assert.False(t, permission.IsError, "用户拒绝是正常权限结果，不能伪造工具失败")
		var value string
		if json.Unmarshal(permission.Content, &value) != nil {
			var content []struct{ Type, Text string }
			if !assert.NoError(t, json.Unmarshal(permission.Content, &content)) {
				return
			}
			for _, item := range content {
				if item.Type == "text" {
					value += item.Text
				}
			}
		}
		var result struct {
			Permissions struct {
				FileSystem *struct{ Write []string }
				Network    *struct{ Enabled bool }
			}
			Scope string
		}
		if !assert.NoError(t, json.Unmarshal([]byte(value), &result)) {
			return
		}
		assert.Equal(t, active.scope, result.Scope)
		assert.True(t, result.Permissions.Network == nil || !result.Permissions.Network.Enabled,
			"非法 Desktop 扩权答案不得成为赢家")
		if active.allowed {
			if assert.NotNil(t, result.Permissions.FileSystem) {
				assert.Equal(t, []string{s.grantRoot}, result.Permissions.FileSystem.Write)
			}
		} else {
			assert.True(t, result.Permissions.FileSystem == nil || len(result.Permissions.FileSystem.Write) == 0)
		}
	}
	active.writeCalls++
	claudePermissionModelTool(w, active.id+"-write", "Write", map[string]any{
		"file_path": s.path, "content": active.id + "\n",
	})
}

func claudePermissionModelTool(w http.ResponseWriter, id, name string, arguments map[string]any) {
	bootstrapClaudeStart(w)
	bootstrapEvent(w, "content_block_start", map[string]any{"index": 0, "content_block": map[string]any{
		"type": "tool_use", "id": id, "name": name, "input": map[string]any{}}})
	input, _ := json.Marshal(arguments)
	bootstrapEvent(w, "content_block_delta", map[string]any{"index": 0, "delta": map[string]any{
		"type": "input_json_delta", "partial_json": string(input)}})
	bootstrapClaudeEnd(w, "tool_use")
}
