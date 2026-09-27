package interactiveprotocol

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// 三个名称都在固定版本的官方协议中，必须使用同一套表单校验。
func TestMcpNativeFormModes(t *testing.T) {
	for _, mode := range []string{"form", "openai/form", "openaiForm"} {
		t.Run(mode, func(t *testing.T) {
			var request map[string]any
			require.NoError(t, json.Unmarshal(formRequest(t), &request))
			request["mode"] = mode
			params := elicitationJSON(t, request)
			questions, err := Questions(MCPElicitation, params)
			require.NoError(t, err)
			require.Contains(t, string(questions), "mcp:field:count")
			content := map[string]any{"count": 2, "enabled": false, "name": "typed"}
			meta := map[string]any{"fixture": map[string]any{"number": 7}}
			valid := elicitationJSON(t, map[string]any{"action": "accept", "content": content, "_meta": meta})
			answer, err := NormalizeAnswer(MCPElicitation, params, valid)
			require.NoError(t, err)
			require.JSONEq(t, string(valid), string(answer))
			_, err = NormalizeAnswer(MCPElicitation, params, elicitationJSON(t, map[string]any{
				"action": "accept", "content": map[string]any{"count": "2", "enabled": false, "name": "typed"},
			}))
			require.ErrorContains(t, err, "不符合请求 schema")
			for _, action := range []string{"decline", "cancel"} {
				answer, err := NormalizeAnswer(MCPElicitation, params, elicitationJSON(t, map[string]any{"action": action}))
				require.NoError(t, err)
				expected := elicitationJSON(t, map[string]any{"action": action, "content": nil, "_meta": nil})
				require.JSONEq(t, string(expected), string(answer))
			}
			draft := map[string]any{
				"mcp:action":        map[string]any{"answers": []string{"确认并继续"}},
				"mcp:field:count":   map[string]any{"answers": []string{"2"}},
				"mcp:field:enabled": map[string]any{"answers": []string{"false"}},
				"mcp:field:name":    map[string]any{"answers": []string{"typed"}},
			}
			answer, complete, err := NormalizeElicitationDraft(params, elicitationJSON(t, map[string]any{"answers": draft}))
			require.NoError(t, err)
			require.True(t, complete)
			expected := elicitationJSON(t, map[string]any{"action": "accept", "content": content, "_meta": nil})
			require.JSONEq(t, string(expected), string(answer))
		})
	}
}
