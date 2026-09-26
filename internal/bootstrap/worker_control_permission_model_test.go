//go:build integration

package bootstrap

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

type nativePermissionCase struct {
	id, scope                   string
	executeOnly, allowed        bool
	permissionSeen, commandSeen bool
}

type nativePermissionScenario struct {
	mu        sync.Mutex
	active    *nativePermissionCase
	path      string
	grantRoot string
}

// 只脚本化本机模型；权限回调与命令均由固定原生 Codex 产生并执行。
func (s *nativePermissionScenario) serve(t *testing.T, w http.ResponseWriter, req *http.Request) {
	if req.URL.Path != "/v1/responses" {
		http.NotFound(w, req)
		return
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, 8<<20))
	if !assert.NoError(t, err) {
		return
	}
	var payload struct {
		Input []map[string]any
		Tools []map[string]any
	}
	if !assert.NoError(t, json.Unmarshal(body, &payload)) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	active := s.active
	if active == nil || !strings.Contains(string(body), active.id) {
		bootstrapModelText(w, false)
		return
	}
	var permissionResult, commandResult string
	for _, item := range payload.Input {
		if item["type"] != "function_call_output" {
			continue
		}
		value, _ := item["output"].(string)
		switch item["call_id"] {
		case active.id:
			permissionResult = value
		case active.id + "-command":
			commandResult = value
		}
	}
	if commandResult != "" {
		active.commandSeen = true
		if active.allowed {
			assert.Contains(t, commandResult, "Process exited with code 0")
		} else {
			assert.NotContains(t, commandResult, "Process exited with code 0")
		}
		bootstrapModelText(w, false)
		return
	}
	if !active.executeOnly && permissionResult == "" {
		declared := false
		for _, tool := range payload.Tools {
			declared = declared || tool["name"] == "request_permissions"
		}
		assert.True(t, declared, "权限请求必须使用固定 CLI 实际声明的原生工具")
		permissionModelTool(w, active.id, "request_permissions", map[string]any{
			"permissions": map[string]any{"file_system": map[string]any{"write": []string{s.grantRoot}}},
			"reason":      active.id,
		})
		return
	}
	if permissionResult != "" {
		active.permissionSeen = true
		var result struct {
			Permissions map[string]json.RawMessage
			Scope       string
		}
		assert.NoError(t, json.Unmarshal([]byte(permissionResult), &result))
		assert.Equal(t, active.scope, result.Scope)
		if active.allowed {
			assert.Contains(t, string(result.Permissions["file_system"]), s.grantRoot)
		} else {
			assert.NotContains(t, string(result.Permissions["file_system"]), s.grantRoot)
		}
	}
	quoted := "'" + strings.ReplaceAll(s.path, "'", "'\"'\"'") + "'"
	permissionModelTool(w, active.id+"-command", "exec_command", map[string]any{
		"cmd": "printf '" + active.id + "\n' >> " + quoted,
	})
}

func permissionModelTool(w http.ResponseWriter, id, name string, arguments map[string]any) {
	args, _ := json.Marshal(arguments)
	bootstrapEvent(w, "response.created", map[string]any{"response": map[string]any{"id": id}})
	bootstrapEvent(w, "response.output_item.done", map[string]any{"item": map[string]any{
		"id": id, "type": "function_call", "call_id": id, "name": name, "arguments": string(args),
	}})
	bootstrapEvent(w, "response.completed", map[string]any{"response": map[string]any{"id": id,
		"usage": map[string]any{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15,
			"input_tokens_details": nil, "output_tokens_details": nil},
	}})
}
