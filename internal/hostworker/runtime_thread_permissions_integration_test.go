//go:build integration

package hostworker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/stretchr/testify/require"
)

// PERMISSION-007：手机创建时使用的原生设置协议经真实 SSH 保存完整策略。
func verifyRuntimeThreadPermissions(t *testing.T, ctx context.Context, client *codex.SocketClient, root string) {
	t.Helper()
	extra := filepath.Join(root, "extra")
	require.NoError(t, os.MkdirAll(extra, 0o700))
	thread := readSessionThread(t, ctx, client, "thread/start", map[string]any{
		"cwd": root, "sandbox": "workspace-write", "approvalPolicy": "on-request",
	})
	policy := map[string]any{
		"type": "workspaceWrite", "writableRoots": []string{extra}, "networkAccess": false,
		"excludeTmpdirEnvVar": true, "excludeSlashTmp": true,
	}
	require.NoError(t, client.Call(ctx, "thread/settings/update", map[string]any{
		"threadId": thread.ID, "sandboxPolicy": policy, "approvalPolicy": "on-request",
	}, nil))
	// Codex 在首个 Turn 后才有可恢复 rollout；两引擎都生成真实原生历史。
	events := client.Subscribe(codex.ThreadFilter{ThreadID: thread.ID})
	defer events.Close()
	var started struct{ Turn struct{ ID string } }
	require.NoError(t, client.Call(ctx, "turn/start", map[string]any{
		"threadId": thread.ID,
		"input":    []map[string]any{{"type": "text", "text": "保存权限并生成恢复记录"}},
	}, &started))
waitTurn:
	for {
		select {
		case <-ctx.Done():
			t.Fatal("权限验证回合没有终结")
		case event, ok := <-events.Events():
			require.True(t, ok)
			if event.Method != "turn/completed" {
				continue
			}
			var result struct{ Turn struct{ ID, Status string } }
			require.NoError(t, json.Unmarshal(event.Params, &result))
			require.Equal(t, started.Turn.ID, result.Turn.ID)
			require.Equal(t, "completed", result.Turn.Status)
			break waitTurn
		}
	}
	var restored struct {
		ApprovalPolicy string
		Sandbox        struct {
			Type                string
			WritableRoots       []string
			NetworkAccess       bool
			ExcludeTmpdirEnvVar bool
			ExcludeSlashTmp     bool
		}
	}
	require.NoError(t, client.Call(ctx, "thread/resume", map[string]any{"threadId": thread.ID}, &restored))
	require.Equal(t, "workspaceWrite", restored.Sandbox.Type)
	require.Contains(t, restored.Sandbox.WritableRoots, extra)
	require.False(t, restored.Sandbox.NetworkAccess)
	require.True(t, restored.Sandbox.ExcludeTmpdirEnvVar)
	require.True(t, restored.Sandbox.ExcludeSlashTmp)
	require.Equal(t, "on-request", restored.ApprovalPolicy)
	require.NoError(t, client.Call(ctx, "thread/settings/update", map[string]any{
		"threadId": thread.ID, "permissions": ":danger-full-access", "approvalPolicy": "never",
	}, nil))
	require.NoError(t, client.Call(ctx, "thread/resume", map[string]any{"threadId": thread.ID}, &restored))
	require.Equal(t, "dangerFullAccess", restored.Sandbox.Type)
	require.Equal(t, "never", restored.ApprovalPolicy)
}
