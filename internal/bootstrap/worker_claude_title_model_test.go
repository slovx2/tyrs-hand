//go:build integration

package bootstrap

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// CLI 原生标题没有工具，与 Worker 的 StructuredOutput 标题任务分别响应。
// 只匹配 system 中的专用指令，避免把用户输入里提到标题的业务请求误分类。
func bootstrapClaudeNativeTitleResponse(w http.ResponseWriter, body []byte) bool {
	var payload struct {
		System []struct{ Text string } `json:"system"`
		Tools  []json.RawMessage       `json:"tools"`
		Stream bool                    `json:"stream"`
	}
	if json.Unmarshal(body, &payload) != nil || len(payload.Tools) != 0 {
		return false
	}
	for _, block := range payload.System {
		if !strings.Contains(block.Text, "You are naming a coding session") {
			continue
		}
		if payload.Stream {
			bootstrapModelText(w, true)
		} else {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "msg_native_title", "type": "message", "role": "assistant", "model": "mock-claude",
				"content":     []map[string]string{{"type": "text", "text": "Native session"}},
				"stop_reason": "end_turn", "stop_sequence": nil,
				"usage": map[string]int{"input_tokens": 10, "output_tokens": 2},
			})
		}
		return true
	}
	return false
}

func TestBootstrapClaudeNativeTitleResponse(t *testing.T) {
	for _, test := range []struct {
		name, body string
		matched    bool
	}{
		{"原生标题", `{"system":[{"text":"You are naming a coding session."}],"stream":false}`, true},
		{"流式标题", `{"system":[{"text":"You are naming a coding session."}],"stream":true}`, true},
		{"用户引用", `{"messages":[{"role":"user","content":"You are naming a coding session"}]}`, false},
		{"业务工具", `{"system":[{"text":"You are naming a coding session."}],"tools":[{"name":"Bash"}]}`, false},
		{"Worker标题", `{"tools":[{"name":"StructuredOutput"}]}`, false},
		{"无效请求", `{`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			require.Equal(t, test.matched, bootstrapClaudeNativeTitleResponse(response, []byte(test.body)))
			if !test.matched {
				require.Empty(t, response.Body.String())
			} else if test.name == "原生标题" {
				require.Equal(t, "application/json", response.Header().Get("Content-Type"))
				var message struct{ Content json.RawMessage }
				require.NoError(t, json.Unmarshal(response.Body.Bytes(), &message))
				require.JSONEq(t, `[{"type":"text","text":"Native session"}]`, string(message.Content))
			} else {
				require.Contains(t, response.Body.String(), "event: message_stop")
			}
		})
	}
}
