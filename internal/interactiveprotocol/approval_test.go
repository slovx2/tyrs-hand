package interactiveprotocol

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApprovalAnswersRequireExplicitNativeDecisions(t *testing.T) {
	for _, method := range []string{CommandApproval, FileApproval} {
		for label, decision := range map[string]string{"允许本次": "accept", "拒绝": "decline", "取消回合": "cancel"} {
			answer, _ := json.Marshal(map[string]any{"answers": map[string]any{"approval": map[string]any{"answers": []string{label}}}})
			result, err := NormalizeAnswer(method, json.RawMessage(`{}`), answer)
			require.NoError(t, err)
			require.JSONEq(t, `{"decision":"`+decision+`"}`, string(result))
			require.Equal(t, label, AnswerLabel(result))
		}
		for _, raw := range []string{`{}`, `null`, `{"decision":null}`, `{"decision":"yes"}`,
			`{"answers":{}}`, `{"answers":{"approval":{"answers":["完全访问"]}}}`,
			`{"answers":{"approval":{"answers":["允许本次","拒绝"]}}}`,
			`{"answers":{"other":{"answers":["允许本次"]}}}`,
			`{"decision":"accept","answers":{}}`,
		} {
			_, err := NormalizeAnswer(method, json.RawMessage(`{}`), json.RawMessage(raw))
			require.Error(t, err, raw)
		}
	}
	_, err := NormalizeAnswer("unknown/approval", json.RawMessage(`{}`), json.RawMessage(`{"decision":"accept"}`))
	require.Error(t, err)
}

func TestApprovalQuestionsReflectNativeAvailableDecisions(t *testing.T) {
	params := json.RawMessage(`{"command":"printf approved","cwd":"/tmp/work","reason":"请求确认","availableDecisions":["decline","cancel"]}`)
	questions, err := Questions(CommandApproval, params)
	require.NoError(t, err)
	require.Contains(t, string(questions), "printf approved")
	require.Contains(t, string(questions), "/tmp/work")
	require.Contains(t, string(questions), "请求确认")
	require.NotContains(t, string(questions), "允许本次")
	_, err = NormalizeAnswer(CommandApproval, params, json.RawMessage(`{"decision":"accept"}`))
	require.Error(t, err)
	_, err = Questions(CommandApproval, json.RawMessage(`{"availableDecisions":["invalid"]}`))
	require.Error(t, err)
}

func TestApprovalPolicyAmendmentsCannotExpandNativeProposal(t *testing.T) {
	params := json.RawMessage(`{"proposedExecpolicyAmendment":["git","status"],"proposedNetworkPolicyAmendments":[{"host":"localhost","action":"allow"}]}`)
	for _, answer := range []string{
		`{"decision":{"acceptWithExecpolicyAmendment":{"execpolicy_amendment":["git","status"]}}}`,
		`{"decision":{"applyNetworkPolicyAmendment":{"network_policy_amendment":{"host":"localhost","action":"allow"}}}}`,
	} {
		result, err := NormalizeAnswer(CommandApproval, params, json.RawMessage(answer))
		require.NoError(t, err)
		require.JSONEq(t, answer, string(result))
		_, err = NormalizeAnswer(FileApproval, params, json.RawMessage(answer))
		require.Error(t, err)
	}
	for _, answer := range []string{
		`{"decision":{"acceptWithExecpolicyAmendment":{"execpolicy_amendment":["git"]}}}`,
		`{"decision":{"applyNetworkPolicyAmendment":{"network_policy_amendment":{"host":"*","action":"allow"}}}}`,
		`{"decision":{"newKind":true}}`,
		`{"decision":{"acceptWithExecpolicyAmendment":{"execpolicy_amendment":["git","status"],"scope":"session"}}}`,
		`{"decision":{"applyNetworkPolicyAmendment":{"network_policy_amendment":{"host":"localhost","action":"allow"},"allowAll":true}}}`,
	} {
		_, err := NormalizeAnswer(CommandApproval, params, json.RawMessage(answer))
		require.Error(t, err)
	}
}

func TestApprovalRejectsInvalidIdentityAndEmptyDecisions(t *testing.T) {
	for _, id := range []string{`0`, `-1`, `123`, `"123"`, `""`} {
		require.True(t, ValidRequestID(json.RawMessage(id)), id)
	}
	for _, id := range []string{`null`, `{}`, `[]`, `true`, `1.5`, `1 2`, ``} {
		require.False(t, ValidRequestID(json.RawMessage(id)), id)
	}
	for _, params := range []string{`null`, `[]`, `{"availableDecisions":[]}`} {
		_, err := NormalizeAnswer(CommandApproval, json.RawMessage(params), json.RawMessage(`{"decision":"accept"}`))
		require.Error(t, err, params)
		_, err = Questions(CommandApproval, json.RawMessage(params))
		require.Error(t, err, params)
	}
}
