package interactiveprotocol

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStdinApprovalPreservesKindInputAndNativeDecisions(t *testing.T) {
	params, err := json.Marshal(map[string]any{
		"kind": "writeStdin", "command": "write_stdin 123 'printf ok\n'",
		"cwd": "/tmp/project", "reason": "终端仍持有上一回合权限",
		"availableDecisions": []string{"accept", "cancel"},
	})
	require.NoError(t, err)
	questions, err := Questions(CommandApproval, params)
	require.NoError(t, err)
	var items []struct {
		Header, Question string
		Options          []struct{ Label string }
	}
	require.NoError(t, json.Unmarshal(questions, &items))
	require.Len(t, items, 1)
	require.Equal(t, "终端输入审批", items[0].Header)
	require.Contains(t, items[0].Question, "向正在运行的终端发送输入")
	require.Contains(t, items[0].Question, "write_stdin 123 'printf ok\n'")
	require.Contains(t, items[0].Question, "终端仍持有上一回合权限")
	require.Equal(t, []struct{ Label string }{{"允许本次"}, {"取消回合"}}, items[0].Options)
	answer, _ := json.Marshal(map[string]string{"decision": "acceptForSession"})
	_, err = NormalizeAnswer(CommandApproval, params, answer)
	require.ErrorContains(t, err, "未被原生请求提供")
}
