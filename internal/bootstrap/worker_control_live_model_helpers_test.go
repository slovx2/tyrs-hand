//go:build integration

package bootstrap

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

type controlLiveModel struct {
	mu                      sync.Mutex
	path                    string
	codexCalls, claudeCalls atomic.Int64
	titleCalls              atomic.Int64
	contextSeen, resultSeen atomic.Bool
}

func (m *controlLiveModel) setPath(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.path = path
}

func (m *controlLiveModel) pathValue() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.path
}

func (m *controlLiveModel) respond(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	if strings.Contains(r.URL.Path, "count_tokens") {
		_, _ = io.WriteString(w, `{"input_tokens":10}`)
		return
	}
	if r.URL.Path == "/api/hello" {
		_, _ = io.WriteString(w, `{}`)
		return
	}
	if r.URL.Path == "/v1/messages" {
		m.claudeCalls.Add(1)
		t.Error("Claude Live 拒绝路径不允许调用模型")
		http.Error(w, "unexpected Claude execution", http.StatusBadRequest)
		return
	}
	if r.URL.Path != "/v1/responses" {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	require.NoError(t, err)
	var payload struct {
		Input json.RawMessage
		Tools []struct{ Name string }
	}
	require.NoError(t, json.Unmarshal(body, &payload))
	if strings.Contains(string(payload.Input), "/session-title-tasks/") {
		m.titleCalls.Add(1)
		controlLiveText(w, `{"title":"Live 接力回归"}`)
		return
	}
	switch step := m.codexCalls.Add(1); step {
	case 1:
		require.True(t, strings.Contains(string(payload.Input), "LIVE_SSH_CONTEXT"), "缺少 SSH 首轮输入")
		controlLiveText(w, "BOOTSTRAP_OK")
	case 2:
		// 只检查真实 input，系统提示和工具声明不能冒充接力历史。
		require.True(t, strings.Contains(string(payload.Input), "LIVE_SSH_CONTEXT"), "缺少 SSH 历史输入")
		require.True(t, strings.Contains(string(payload.Input), "BOOTSTRAP_OK"), "缺少 SSH 历史回复")
		require.True(t, strings.Contains(string(payload.Input), "LIVE_HANDOFF_WRITE"), "缺少 Live handoff 输入")
		m.contextSeen.Store(true)
		declared := false
		for _, tool := range payload.Tools {
			declared = declared || tool.Name == "exec_command"
		}
		require.True(t, declared, "真实 CLI 必须声明原生命令工具")
		path := "'" + strings.ReplaceAll(m.pathValue(), "'", "'\"'\"'") + "'"
		arguments, err := json.Marshal(map[string]any{"cmd": fmt.Sprintf("printf 'LIVE_EFFECT\\n' >> %s; cat %s", path, path),
			"yield_time_ms": 1000, "max_output_tokens": 1000})
		require.NoError(t, err)
		bootstrapEvent(w, "response.created", map[string]any{"response": map[string]any{"id": "resp_live_tool"}})
		bootstrapEvent(w, "response.output_item.done", map[string]any{"item": map[string]any{
			"type": "function_call", "id": "fc_live", "call_id": "live-native-tool", "name": "exec_command", "arguments": string(arguments)}})
		controlLiveCompleted(w, "resp_live_tool")
	case 3:
		var input []struct {
			Type   string          `json:"type"`
			CallID string          `json:"call_id"`
			Output json.RawMessage `json:"output"`
		}
		require.NoError(t, json.Unmarshal(payload.Input, &input))
		found := false
		for _, item := range input {
			if item.Type == "function_call_output" && item.CallID == "live-native-tool" {
				require.Contains(t, string(item.Output), "LIVE_EFFECT")
				found = true
			}
		}
		require.True(t, found, "真实工具输出必须返回模型上下文")
		content, err := os.ReadFile(m.pathValue())
		require.NoError(t, err)
		require.Equal(t, "LIVE_EFFECT\n", string(content))
		m.resultSeen.Store(true)
		controlLiveText(w, "LIVE_HANDOFF_DONE")
	default:
		t.Errorf("Live 意外重复执行模型，第 %d 次", step)
		http.Error(w, "unexpected repeated execution", http.StatusBadRequest)
	}
}

func controlLiveText(w http.ResponseWriter, text string) {
	bootstrapEvent(w, "response.created", map[string]any{"response": map[string]any{"id": "resp_live_text"}})
	bootstrapEvent(w, "response.output_item.done", map[string]any{"item": map[string]any{
		"id": "msg_live", "type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": text}}}})
	controlLiveCompleted(w, "resp_live_text")
}

func controlLiveCompleted(w http.ResponseWriter, id string) {
	bootstrapEvent(w, "response.completed", map[string]any{"response": map[string]any{"id": id,
		"usage": map[string]any{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15, "input_tokens_details": nil, "output_tokens_details": nil}}})
}
