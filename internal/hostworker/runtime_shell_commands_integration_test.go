//go:build integration

package hostworker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
)

func TestRuntimeShellCommandsRealSSHBothEngines(t *testing.T) {
	testRuntimeRegistryRealSSH(t, "shell-commands")
}

// SHELL-002：手动 shell 的固定协议要求完全访问，但必须绑定真实有效的所属会话。
func verifyRuntimeShellCommands(t *testing.T, ctx context.Context, clients map[runtimeidentity.Engine]*codex.SocketClient, root string) {
	t.Helper()
	threads := map[runtimeidentity.Engine]string{}
	for _, engine := range []runtimeidentity.Engine{runtimeidentity.Codex, runtimeidentity.Claude} {
		cwd := filepath.Join(root, "project", "shell-"+string(engine))
		require.NoError(t, os.MkdirAll(cwd, 0o700))
		thread := readSessionThread(t, ctx, clients[engine], "thread/start", map[string]any{
			"cwd": cwd, "sandbox": "read-only", "approvalPolicy": "never",
		})
		threads[engine] = thread.ID
	}
	for _, engine := range []runtimeidentity.Engine{runtimeidentity.Codex, runtimeidentity.Claude} {
		t.Run(string(engine), func(t *testing.T) {
			client := clients[engine]
			other := runtimeidentity.Claude
			if engine == runtimeidentity.Claude {
				other = runtimeidentity.Codex
			}
			cwd := filepath.Join(root, "project", "shell-"+string(engine))
			marker := filepath.Join(cwd, "invalid.txt")
			for _, threadID := range []string{"does-not-exist", threads[other]} {
				var response any
				var rpcError *codex.RPCError
				err := client.Call(ctx, "thread/shellCommand", map[string]string{
					"threadId": threadID, "command": "printf 'INVALID' > '" + marker + "'",
				}, &response)
				require.ErrorAs(t, err, &rpcError)
				require.Nil(t, response)
			}
			result := filepath.Join(cwd, "actual.txt")
			require.NoError(t, client.Call(ctx, "thread/shellCommand", map[string]string{
				"threadId": threads[engine],
				"command":  "printf 'ACTUAL' | tr '[:upper:]' '[:lower:]' > actual.txt",
			}, nil))
			require.Eventually(t, func() bool {
				contents, err := os.ReadFile(result)
				return err == nil && string(contents) == "actual"
			}, 5*time.Second, 20*time.Millisecond, "真实 shell 必须在所属会话 cwd 产生文件")
			_, err := os.Stat(marker)
			require.True(t, os.IsNotExist(err), "未知会话及另一引擎会话不得执行命令")
		})
	}
}
