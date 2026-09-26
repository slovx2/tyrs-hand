//go:build integration

package hostworker

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestRuntimeExperimentalFeaturesRealSSH(t *testing.T) {
	testRuntimeRegistryRealSSH(t, "experimental-features")
}

// FEATURE-002：真实 SSH 目录、空操作与参数错误透传，不伪造开关成功或配置副作用。
func verifyRuntimeExperimentalFeatures(t *testing.T, ctx context.Context, connection *ssh.Client, cwd string) {
	t.Helper()
	client, trace := connectRuntimeSSHWithTrace(t, ctx, connection, runtimeidentity.Claude, codex.SocketClientOptions{})
	var written struct{ FilePath string }
	require.NoError(t, client.Call(ctx, "config/value/write", map[string]any{
		"keyPath": "sandbox_mode", "value": "read-only", "mergeStrategy": "replace",
	}, &written))
	beforeFile, err := os.ReadFile(written.FilePath)
	require.NoError(t, err)
	var beforeConfig json.RawMessage
	require.NoError(t, client.Call(ctx, "config/read", map[string]any{"includeLayers": true}, &beforeConfig))
	list := func(params map[string]any) {
		var result json.RawMessage
		require.NoError(t, client.Call(ctx, "experimentalFeature/list", params, &result))
		require.JSONEq(t, `{"data":[],"nextCursor":null}`, string(result))
	}
	list(map[string]any{})
	for _, limit := range []any{nil, 0, 1001, uint64(4294967295)} {
		list(map[string]any{"limit": limit, "cursor": nil})
	}
	thread := readSessionThread(t, ctx, client, "thread/start", map[string]any{"cwd": cwd})
	list(map[string]any{"threadId": thread.ID})
	var enabled json.RawMessage
	require.NoError(t, client.Call(ctx, "experimentalFeature/enablement/set", map[string]any{"enablement": map[string]bool{}}, &enabled))
	require.JSONEq(t, `{"enablement":{}}`, string(enabled))
	for _, test := range []struct {
		method        string
		params        map[string]any
		code          int
		invalidSchema bool
	}{
		{"experimentalFeature/list", map[string]any{"cursor": "unissued-cursor"}, -32602, false},
		{"experimentalFeature/list", map[string]any{"threadId": "unknown-thread"}, -32602, false},
		{"experimentalFeature/list", map[string]any{"limit": "bad"}, -32602, true},
		{"experimentalFeature/enablement/set", map[string]any{"enablement": map[string]any{"memories": true}}, -32004, false},
		{"experimentalFeature/enablement/set", map[string]any{"enablement": map[string]any{"memories": false}}, -32004, false},
		{"experimentalFeature/enablement/set", map[string]any{"enablement": map[string]any{"unknown": "invalid"}}, -32602, true},
		{"experimentalFeature/enablement/set", map[string]any{}, -32602, true},
	} {
		if test.invalidSchema {
			trace.expectParameterError(test.method)
		}
		var result any
		var rpcError *codex.RPCError
		require.ErrorAs(t, client.Call(ctx, test.method, test.params, &result), &rpcError)
		require.Equal(t, test.code, rpcError.Code)
		require.Nil(t, result, "错误不能同时返回成功内容")
	}
	require.NoError(t, client.Call(ctx, "thread/unsubscribe", map[string]string{"threadId": thread.ID}, nil))
	var loaded struct{ Data []string }
	require.NoError(t, client.Call(ctx, "thread/loaded/list", map[string]any{}, &loaded))
	var rpcError *codex.RPCError
	// Hub 可以为其他消费者保留原生会话；目录身份必须与真实 loaded 状态一致。
	if slices.Contains(loaded.Data, thread.ID) {
		list(map[string]any{"threadId": thread.ID})
	} else {
		require.ErrorAs(t, client.Call(ctx, "experimentalFeature/list", map[string]string{"threadId": thread.ID}, nil), &rpcError)
		require.Equal(t, -32602, rpcError.Code)
	}
	list(map[string]any{})
	readSessionThread(t, ctx, client, "thread/read", map[string]any{"threadId": thread.ID})
	require.NoError(t, client.Call(ctx, "thread/delete", map[string]string{"threadId": thread.ID}, nil))
	require.ErrorAs(t, client.Call(ctx, "experimentalFeature/list", map[string]string{"threadId": thread.ID}, nil), &rpcError)
	require.Equal(t, -32602, rpcError.Code)
	var afterConfig json.RawMessage
	require.NoError(t, client.Call(ctx, "config/read", map[string]any{"includeLayers": true}, &afterConfig))
	require.JSONEq(t, string(beforeConfig), string(afterConfig), "错误和空操作不改变配置内容或版本")
	afterFile, err := os.ReadFile(written.FilePath)
	require.NoError(t, err)
	require.Equal(t, beforeFile, afterFile, "必须检查真实配置文件未改写")
}
