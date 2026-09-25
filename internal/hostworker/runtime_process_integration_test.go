//go:build integration

package hostworker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/stretchr/testify/require"
)

func verifyRuntimeProcesses(t *testing.T, ctx context.Context, client *codex.SocketClient, root string) {
	t.Helper()
	full := map[string]any{"type": "dangerFullAccess"}
	var buffered struct {
		ExitCode       int `json:"exitCode"`
		Stdout, Stderr string
	}
	require.NoError(t, client.Call(ctx, "command/exec", map[string]any{"cwd": root, "sandboxPolicy": full,
		"command": []string{"/bin/sh", "-c", "printf abcdef; printf stderr >&2; exit 7"}, "outputBytesCap": 3}, &buffered))
	require.Equal(t, 7, buffered.ExitCode)
	require.Equal(t, "abc", buffered.Stdout)
	require.Equal(t, "std", buffered.Stderr)
	events := client.Subscribe(codex.ThreadFilter{})
	defer events.Close()
	await := func(method, field, id, text string) map[string]any {
		t.Helper()
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		for {
			select {
			case event := <-events.Events():
				var params map[string]any
				require.NoError(t, json.Unmarshal(event.Params, &params))
				if event.Method != method || params[field] != id {
					continue
				}
				if text != "" {
					data, err := base64.StdEncoding.DecodeString(params["deltaBase64"].(string))
					require.NoError(t, err)
					if !strings.Contains(string(data), text) {
						continue
					}
				}
				return params
			case <-deadline.C:
				t.Fatalf("未收到 %s %s %s", method, id, text)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
	}
	var ignored any
	for _, kind := range []string{"command", "process"} {
		id := "terminal-" + kind
		field, start, delta, write, resize := "processId", "command/exec", "command/exec/outputDelta", "command/exec/write", "command/exec/resize"
		if kind == "process" {
			field, start, delta, write, resize = "processHandle", "process/spawn", "process/outputDelta", "process/writeStdin", "process/resizePty"
		}
		params := map[string]any{field: id, "cwd": root, "tty": true, "size": map[string]int{"rows": 23, "cols": 71},
			"command": []string{"/bin/sh", "-c", "stty -echo; stty size; read line; stty size; exit 7"}}
		if kind == "command" {
			params["sandboxPolicy"] = full
		}
		result := make(chan error, 1)
		var exit struct {
			ExitCode int `json:"exitCode"`
		}
		go func() { result <- client.Call(ctx, start, params, &exit) }()
		if kind == "process" {
			require.NoError(t, <-result)
		}
		await(delta, field, id, "23 71")
		require.NoError(t, client.Call(ctx, resize, map[string]any{field: id, "size": map[string]int{"rows": 39, "cols": 107}}, &ignored))
		require.NoError(t, client.Call(ctx, write, map[string]any{field: id, "deltaBase64": base64.StdEncoding.EncodeToString([]byte("continue\n"))}, &ignored))
		await(delta, field, id, "39 107")
		if kind == "command" {
			require.NoError(t, <-result)
			require.Equal(t, 7, exit.ExitCode)
		} else {
			require.Equal(t, float64(7), await("process/exited", field, id, "")["exitCode"])
		}
	}
	// 真实文件副作用证明 stdin 到达子进程，二进制内容不经过文本转换。
	payload := []byte{0, 128, 255, 10}
	file := filepath.Join(root, "process-input.bin")
	require.NoError(t, client.Call(ctx, "process/spawn", map[string]any{"processHandle": "binary", "cwd": root,
		"command": []string{"/bin/sh", "-c", "cat > process-input.bin"}, "streamStdin": true}, &ignored))
	require.NoError(t, client.Call(ctx, "process/writeStdin", map[string]any{"processHandle": "binary",
		"deltaBase64": base64.StdEncoding.EncodeToString(payload), "closeStdin": true}, &ignored))
	require.Equal(t, float64(0), await("process/exited", "processHandle", "binary", "")["exitCode"])
	actual, err := os.ReadFile(file)
	require.NoError(t, err)
	require.Equal(t, payload, actual)
	require.NoError(t, client.Call(ctx, "process/spawn", map[string]any{"processHandle": "kill", "cwd": root,
		"command": []string{"/bin/sh", "-c", "printf READY; sleep 30; touch unexpected"}, "streamStdoutStderr": true}, &ignored))
	await("process/outputDelta", "processHandle", "kill", "READY")
	require.NoError(t, client.Call(ctx, "process/kill", map[string]any{"processHandle": "kill"}, &ignored))
	require.NotEqual(t, float64(0), await("process/exited", "processHandle", "kill", "")["exitCode"])
	_, err = os.Stat(filepath.Join(root, "unexpected"))
	require.True(t, os.IsNotExist(err))
	commandDone := make(chan error, 1)
	go func() {
		commandDone <- client.Call(ctx, "command/exec", map[string]any{"processId": "stop-command", "cwd": root,
			"sandboxPolicy": full, "command": []string{"/bin/sh", "-c", "printf READY; sleep 30"}, "streamStdoutStderr": true}, &buffered)
	}()
	await("command/exec/outputDelta", "processId", "stop-command", "READY")
	require.NoError(t, client.Call(ctx, "command/exec/terminate", map[string]any{"processId": "stop-command"}, &ignored))
	require.NoError(t, <-commandDone)
	require.NotEqual(t, 0, buffered.ExitCode)
}

func verifyRuntimeProcessIsolation(t *testing.T, ctx context.Context, first, second *codex.SocketClient, root string) {
	t.Helper()
	clients := []*codex.SocketClient{first, second}
	events := make([]*codex.EventSubscription, 2)
	var ignored any
	for index, client := range clients {
		events[index] = client.Subscribe(codex.ThreadFilter{})
		defer events[index].Close()
		require.NoError(t, client.Call(ctx, "process/spawn", map[string]any{"processHandle": "same-process", "cwd": root,
			"command": []string{"/bin/sh", "-c", "read line; printf '%s' \"$line\""}, "streamStdin": true, "streamStdoutStderr": true}, &ignored))
	}
	// 第一个连接的断开必须终止自己的进程，第二个相同 ID 仍可继续输入。
	require.NoError(t, first.Close())
	require.NoError(t, second.Call(ctx, "process/writeStdin", map[string]any{"processHandle": "same-process",
		"deltaBase64": base64.StdEncoding.EncodeToString([]byte("SECOND-ONLY\n"))}, &ignored))
	var output strings.Builder
	deadline := time.After(5 * time.Second)
	for {
		select {
		case event := <-events[1].Events():
			var params map[string]any
			require.NoError(t, json.Unmarshal(event.Params, &params))
			require.Equal(t, "same-process", params["processHandle"])
			if event.Method == "process/outputDelta" {
				data, err := base64.StdEncoding.DecodeString(params["deltaBase64"].(string))
				require.NoError(t, err)
				output.Write(data)
			}
			if event.Method == "process/exited" {
				require.Equal(t, float64(0), params["exitCode"])
				require.Equal(t, "SECOND-ONLY", output.String())
				return
			}
		case <-deadline:
			t.Fatal("另一连接断开后同名进程未能继续")
		}
	}
}

func verifyRuntimeCommandPermissions(t *testing.T, ctx context.Context, client *codex.SocketClient, root, modelURL string) {
	t.Helper()
	workspace := filepath.Join(root, "command-workspace")
	require.NoError(t, os.MkdirAll(workspace, 0o700))
	run := func(command []string, policy map[string]any) int {
		t.Helper()
		var result struct {
			ExitCode int `json:"exitCode"`
			Stderr   string
		}
		require.NoError(t, client.Call(ctx, "command/exec", map[string]any{"cwd": workspace, "command": command, "sandboxPolicy": policy}, &result))
		t.Logf("command 权限结果: %d %s", result.ExitCode, result.Stderr)
		return result.ExitCode
	}
	require.NotEqual(t, 0, run([]string{"/bin/sh", "-c", "printf forbidden > readonly.txt"}, map[string]any{"type": "readOnly"}))
	_, err := os.Stat(filepath.Join(workspace, "readonly.txt"))
	require.True(t, os.IsNotExist(err))
	policy := map[string]any{"type": "workspaceWrite", "writableRoots": []string{workspace}, "excludeSlashTmp": true, "excludeTmpdirEnvVar": true, "networkAccess": false}
	require.Equal(t, 0, run([]string{"/bin/sh", "-c", "printf permitted > inside.txt"}, policy))
	actual, err := os.ReadFile(filepath.Join(workspace, "inside.txt"))
	require.NoError(t, err)
	require.Equal(t, "permitted", string(actual))
	require.NotEqual(t, 0, run([]string{"/bin/sh", "-c", "printf forbidden > ../outside.txt"}, policy))
	_, err = os.Stat(filepath.Join(root, "outside.txt"))
	require.True(t, os.IsNotExist(err))
	command := []string{"/usr/bin/curl", "-fsS", "--max-time", "2", modelURL + "/api/hello"}
	require.Equal(t, 0, run(command, map[string]any{"type": "dangerFullAccess"}))
	require.NotEqual(t, 0, run(command, policy), "工作区沙箱必须禁止网络访问")
}
