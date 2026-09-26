package interactiveprotocol

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPermissionAnswerCannotExpandNativeProposal(t *testing.T) {
	request := json.RawMessage("{\"permissions\":{\"network\":null,\"fileSystem\":{\"read\":null,\"write\":[\"/workspace\"],\"entries\":[{\"path\":{\"type\":\"path\",\"path\":\"/workspace\"},\"access\":\"write\"}]}}}")
	for _, test := range []struct {
		name, answer string
		valid        bool
	}{
		{"拒绝", "{\"permissions\":{},\"scope\":\"turn\"}", true},
		{"本轮", "{\"permissions\":{\"fileSystem\":{\"read\":null,\"write\":[\"/workspace\"]}},\"scope\":\"turn\"}", true},
		{"降为只读", "{\"permissions\":{\"fileSystem\":{\"read\":[\"/workspace\"],\"write\":null}},\"scope\":\"turn\"}", true},
		{"显式本会话", "{\"permissions\":{},\"scope\":\"session\",\"strictAutoReview\":true}", true},
		{"新增路径", "{\"permissions\":{\"fileSystem\":{\"read\":null,\"write\":[\"/\"]}},\"scope\":\"turn\"}", false},
		{"新增网络", "{\"permissions\":{\"network\":{\"enabled\":true}},\"scope\":\"turn\"}", false},
		{"缺少权限", "{\"scope\":\"turn\"}", false},
		{"空权限不是拒绝对象", "{\"permissions\":null,\"scope\":\"turn\"}", false},
		{"缺少作用域", "{\"permissions\":{}}", false},
		{"未知作用域", "{\"permissions\":{},\"scope\":\"always\"}", false},
		{"未知权限", "{\"permissions\":{\"dangerFullAccess\":true},\"scope\":\"turn\"}", false},
		{"未知顶层参数", "{\"permissions\":{},\"scope\":\"turn\",\"always\":true}", false},
		{"选项本轮", "{\"answers\":{\"approval\":{\"answers\":[\"允许本轮\"]}}}", true},
		{"选项拒绝", "{\"answers\":{\"approval\":{\"answers\":[\"拒绝\"]}}}", true},
		{"冲突回答", "{\"answers\":{\"approval\":{\"answers\":[\"拒绝\"]}},\"permissions\":{}}", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			answer, err := normalizePermissionAnswer(request, json.RawMessage(test.answer))
			if !test.valid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			var decoded permissionAnswer
			require.NoError(t, json.Unmarshal(answer, &decoded))
			require.Contains(t, []string{"turn", "session"}, decoded.Scope)
		})
	}
}

func TestPermissionAnswerRetainsDeniedEntries(t *testing.T) {
	request := json.RawMessage("{\"permissions\":{\"fileSystem\":{\"read\":null,\"write\":[\"/workspace\"],\"entries\":[{\"path\":{\"type\":\"path\",\"path\":\"/workspace/secrets\"},\"access\":\"deny\"}]}}}")
	_, err := normalizePermissionAnswer(request, json.RawMessage("{\"permissions\":{\"fileSystem\":{\"read\":null,\"write\":[\"/workspace\"]}},\"scope\":\"turn\"}"))
	require.Error(t, err)
	answer, err := normalizePermissionAnswer(request, json.RawMessage("{\"answers\":{\"approval\":{\"answers\":[\"允许本会话\"]}}}"))
	require.NoError(t, err)
	require.Contains(t, string(answer), "deny")
	require.Contains(t, string(answer), "session")
}

func TestPermissionAnswerRejectsMalformedDeniedPath(t *testing.T) {
	for _, path := range []string{`null`, `42`, `{}`, `{"type":"path","path":null}`,
		`{"type":"path","path":"/secret","extra":true}`, `{"type":"special","value":{"kind":"unknown"}}`} {
		answer := json.RawMessage(`{"permissions":{"fileSystem":{"read":null,"write":null,"entries":[{"path":` + path + `,"access":"deny"}]}},"scope":"turn"}`)
		_, err := normalizePermissionAnswer(json.RawMessage(`{"permissions":{"fileSystem":{"read":null,"write":null}}}`), answer)
		require.Error(t, err, path)
	}
	for _, path := range []string{`{"type":"path","path":"/secret"}`,
		`{"type":"glob_pattern","pattern":"**/secret"}`, `{"type":"special","value":{"kind":"root"}}`,
		`{"type":"special","value":{"kind":"project_roots","subpath":null}}`,
		`{"type":"special","value":{"kind":"unknown","path":"/future","subpath":"child"}}`} {
		require.True(t, validPermissionPath(json.RawMessage(path)), path)
	}
}
