//go:build integration

package hostworker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/stretchr/testify/require"
)

// 经真实 SSH/Hub 校验明确拒绝；同一个客户端随后必须仍可正常读取能力。
func verifyClaudeInapplicableCapabilities(t *testing.T, ctx context.Context, client *codex.SocketClient) {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	require.True(t, ok)
	data, err := os.ReadFile(filepath.Join(filepath.Dir(source), "..", "..", "protocol", "claude-inapplicable-requests.json"))
	require.NoError(t, err)
	var cases []struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	require.NoError(t, json.Unmarshal(data, &cases))
	require.Len(t, cases, 43)
	// 带会话范围的请求须使用本连接真实可见的会话，Hub 校验通过后才由适配器判定是否适用；临时会话不进入会话目录。
	var thread struct{ Thread struct{ ID string } }
	require.NoError(t, client.Call(ctx, "thread/start", map[string]any{
		"cwd": os.TempDir(), "approvalPolicy": "never", "sandbox": "read-only", "ephemeral": true}, &thread))
	for _, item := range cases {
		params := item.Params
		var scoped map[string]json.RawMessage
		if json.Unmarshal(params, &scoped) == nil {
			if _, ok := scoped["threadId"]; ok {
				scoped["threadId"], _ = json.Marshal(thread.Thread.ID)
				params, _ = json.Marshal(scoped)
			}
		}
		var result any
		err := client.Call(ctx, item.Method, params, &result)
		var rpcErr *codex.RPCError
		require.ErrorAs(t, err, &rpcErr, item.Method)
		require.Equal(t, -32004, rpcErr.Code, item.Method)
		require.Nil(t, result, "拒绝不能伪装成空成功")
	}
	var account struct {
		Account            any  `json:"account"`
		RequiresOpenAIAuth bool `json:"requiresOpenaiAuth"`
	}
	require.NoError(t, client.Call(ctx, "account/read", map[string]any{}, &account))
	require.Nil(t, account.Account)
	require.False(t, account.RequiresOpenAIAuth)
	var ignored any
	var rpcErr *codex.RPCError
	require.ErrorAs(t, client.Call(ctx, "unknown/protocol", map[string]any{}, &ignored), &rpcErr)
	require.Equal(t, -32601, rpcErr.Code)
}
