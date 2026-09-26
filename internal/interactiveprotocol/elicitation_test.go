package interactiveprotocol

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func elicitationJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return raw
}

func formRequest(t *testing.T) json.RawMessage {
	return elicitationJSON(t, map[string]any{"mode": "form", "message": "表单", "requestedSchema": map[string]any{
		"type": "object", "properties": map[string]any{
			"count":   map[string]any{"type": "integer", "minimum": 1},
			"enabled": map[string]any{"type": "boolean"},
			"name":    map[string]any{"type": "string", "minLength": 1},
		}, "required": []string{"count", "enabled", "name"}, "additionalProperties": false, "minProperties": 3,
	}})
}

func TestMcpNativeAnswersValidateRequestedSchema(t *testing.T) {
	params := formRequest(t)
	for _, test := range []struct {
		name, action string
		content      any
		valid        bool
	}{
		{"正确类型", "accept", map[string]any{"count": 2, "enabled": false, "name": "ok"}, true},
		{"缺少字段", "accept", map[string]any{"count": 2}, false},
		{"数字字符串", "accept", map[string]any{"count": "2", "enabled": true, "name": "ok"}, false},
		{"小数不是整数", "accept", map[string]any{"count": 1.5, "enabled": true, "name": "ok"}, false},
		{"范围之外", "accept", map[string]any{"count": 0, "enabled": true, "name": "ok"}, false},
		{"拒绝", "decline", nil, true},
		{"取消", "cancel", nil, true},
		{"拒绝夹带内容", "decline", map[string]any{"name": "unsafe"}, false},
		{"未知决策", "approve", nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := NormalizeAnswer(MCPElicitation, params, elicitationJSON(t, map[string]any{"action": test.action, "content": test.content, "_meta": nil}))
			if !test.valid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			var answer map[string]any
			require.NoError(t, json.Unmarshal(raw, &answer))
			require.Equal(t, test.action, answer["action"])
			require.Contains(t, answer, "content")
			require.Contains(t, answer, "_meta")
		})
	}
}

func TestMcpDraftPreservesTypesAndRejectsUnknownFields(t *testing.T) {
	params := formRequest(t)
	draft := map[string]any{"mcp:action": map[string]any{"answers": []string{"确认并继续"}}}
	call := func() (json.RawMessage, bool, error) {
		return NormalizeElicitationDraft(params, elicitationJSON(t, map[string]any{"answers": draft}))
	}
	_, complete, err := call()
	require.NoError(t, err)
	require.False(t, complete)
	draft["mcp:field:count"] = map[string]any{"answers": []string{"0"}}
	_, _, err = call()
	require.Error(t, err, "不等填完才发现前面字段范围错误")
	draft["mcp:field:count"] = map[string]any{"answers": []string{"2"}}
	draft["mcp:field:enabled"] = map[string]any{"answers": []string{"false"}}
	draft["mcp:field:name"] = map[string]any{"answers": []string{"typed"}}
	raw, complete, err := call()
	require.NoError(t, err)
	require.True(t, complete)
	var value struct{ Content map[string]any }
	require.NoError(t, json.Unmarshal(raw, &value))
	require.Equal(t, float64(2), value.Content["count"])
	require.Equal(t, false, value.Content["enabled"])
	draft["mcp:field:unknown"] = map[string]any{"answers": []string{"x"}}
	_, _, err = call()
	require.Error(t, err)
	for _, choice := range []string{"拒绝", "取消"} {
		answer, done, answerErr := NormalizeElicitationDraft(params, elicitationJSON(t, map[string]any{"answers": map[string]any{
			"mcp:action": map[string]any{"answers": []string{choice}},
		}}))
		require.NoError(t, answerErr)
		require.True(t, done)
		require.NotContains(t, string(answer), "typed")
	}
}

func TestMcpSchemaCannotReadExternalResources(t *testing.T) {
	var fetched atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fetched.Add(1); _, _ = w.Write([]byte("{}")) }))
	defer server.Close()
	for _, location := range []string{server.URL + "/schema.json", "file:///etc/passwd"} {
		params := elicitationJSON(t, map[string]any{"mode": "openai/form", "requestedSchema": map[string]any{"$ref": location}})
		_, err := Questions(MCPElicitation, params)
		require.Error(t, err)
	}
	require.Zero(t, fetched.Load())
}

func TestMcpURLResponsesKeepNullContent(t *testing.T) {
	params := elicitationJSON(t, map[string]any{"mode": "url", "url": "https://example.invalid/flow", "elicitationId": "flow", "message": "确认"})
	for _, action := range []string{"accept", "decline", "cancel"} {
		_, err := NormalizeAnswer(MCPElicitation, params, elicitationJSON(t, map[string]any{"action": action, "content": nil, "_meta": nil}))
		require.NoError(t, err)
	}
	_, err := NormalizeAnswer(MCPElicitation, params, elicitationJSON(t, map[string]any{"action": "accept", "content": map[string]any{"value": "not-a-form"}}))
	require.Error(t, err)
	for _, meta := range []any{true, "unsafe", []any{"unsafe"}} {
		_, err = NormalizeAnswer(MCPElicitation, params, elicitationJSON(t, map[string]any{"action": "accept", "_meta": meta}))
		require.Error(t, err)
	}
}

func TestMcpEnumTitlesAndTypedDraft(t *testing.T) {
	params := elicitationJSON(t, map[string]any{"mode": "form", "message": "选择", "requestedSchema": map[string]any{
		"type": "object", "properties": map[string]any{
			"priority": map[string]any{"oneOf": []any{map[string]any{"const": 1, "title": "低"}, map[string]any{"const": 2, "title": "高"}}},
			"region":   map[string]any{"type": "string", "enum": []string{"a", "b", "c", "d"}, "enumNames": []string{"甲", "乙", "丙", "丁"}},
			"note":     map[string]any{"type": "string"},
		}, "required": []string{"priority", "region"},
	}})
	questions, err := Questions(MCPElicitation, params)
	require.NoError(t, err)
	require.Contains(t, string(questions), "低")
	require.Contains(t, string(questions), "d：丁")
	answer, complete, err := NormalizeElicitationDraft(params, elicitationJSON(t, map[string]any{"answers": map[string]any{
		"mcp:action":         map[string]any{"answers": []string{"确认并继续"}},
		"mcp:field:priority": map[string]any{"answers": []string{"2"}},
		"mcp:field:region":   map[string]any{"answers": []string{"d"}},
		"mcp:field:note":     map[string]any{"answers": []string{"跳过此项"}},
	}}))
	require.NoError(t, err)
	require.True(t, complete)
	require.JSONEq(t, `{"action":"accept","content":{"priority":2,"region":"d"},"_meta":null}`, string(answer))
}
