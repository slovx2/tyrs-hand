//go:build integration

package hostworker

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/stretchr/testify/require"
)

func TestRuntimeConfigRealSSHBothEngines(t *testing.T) {
	testRuntimeRegistryRealSSH(t, "config")
}

// CONFIG-007：配置经两个真实 SSH 入口写入各自文件，后续会话继承准确权限。
func verifyRuntimeConfig(t *testing.T, ctx context.Context, client *codex.SocketClient, cwd string) {
	t.Helper()
	type layer struct {
		Name    struct{ Type, File string }
		Version string
	}
	type configuration struct {
		Config map[string]any
		Layers []layer
	}
	var initial configuration
	require.NoError(t, client.Call(ctx, "config/read", map[string]any{"includeLayers": true}, &initial))
	var user layer
	for _, candidate := range initial.Layers {
		if candidate.Name.Type == "user" {
			user = candidate
		}
	}
	require.NotEmpty(t, user.Name.File)
	require.NotEmpty(t, user.Version)
	edit := func(key string, value any) map[string]any {
		return map[string]any{"keyPath": key, "value": value, "mergeStrategy": "replace"}
	}
	var written struct{ Status, Version, FilePath string }
	require.NoError(t, client.Call(ctx, "config/batchWrite", map[string]any{
		"expectedVersion": user.Version,
		"edits":           []map[string]any{edit("sandbox_mode", "read-only"), edit("approval_policy", "never")},
	}, &written))
	require.Equal(t, "ok", written.Status)
	require.Equal(t, user.Name.File, written.FilePath)
	require.NotEqual(t, user.Version, written.Version)
	file, err := os.ReadFile(written.FilePath)
	require.NoError(t, err)
	require.Contains(t, string(file), "read-only", "必须写入真实配置文件")
	var current configuration
	require.NoError(t, client.Call(ctx, "config/read", map[string]any{}, &current))
	require.Equal(t, "read-only", current.Config["sandbox_mode"])
	require.Equal(t, "never", current.Config["approval_policy"])
	for _, mode := range []string{"read-only", "danger-full-access"} {
		if mode == "danger-full-access" {
			require.NoError(t, client.Call(ctx, "config/value/write", edit("sandbox_mode", mode), &written))
		}
		var thread struct {
			Thread         struct{ ID string }
			ApprovalPolicy string
			Sandbox        struct{ Type string }
		}
		require.NoError(t, client.Call(ctx, "thread/start", map[string]any{"cwd": cwd}, &thread))
		require.Equal(t, "never", thread.ApprovalPolicy)
		expected := map[string]string{"read-only": "readOnly", "danger-full-access": "dangerFullAccess"}[mode]
		require.Equal(t, expected, thread.Sandbox.Type)
		require.NotEmpty(t, thread.Thread.ID)
	}
	var requirements map[string]json.RawMessage
	require.NoError(t, client.Call(ctx, "configRequirements/read", nil, &requirements))
	require.Contains(t, requirements, "requirements")
	require.NoError(t, client.Call(ctx, "config/read", map[string]any{}, &current))
	require.Equal(t, "danger-full-access", current.Config["sandbox_mode"])
}
