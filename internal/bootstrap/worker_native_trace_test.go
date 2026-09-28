//go:build integration

package bootstrap

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
)

// 在真实 CLI 入口旁路录制，覆盖辅助客户端和 Control 仲裁，不改写协议报文。
// 必须在 InitializeWorker 前调用，让清理函数在 Worker 停止后保存完整证据。
func recordBootstrapCodexUpstream(t *testing.T, cfg *config.Config, method, direction string) {
	t.Helper()
	node, err := exec.LookPath("node")
	require.NoError(t, err)
	_, source, _, ok := runtime.Caller(0)
	require.True(t, ok)
	recorder := filepath.Join(filepath.Dir(source), "../../tools/mobile-e2e/lib/record-runtime.mjs")
	adapter := filepath.Dir(filepath.Dir(cfg.WorkerClaudeBin))
	trace := filepath.Join(cfg.WorkerHome, "codex-upstream.jsonl")
	settings, err := json.Marshal(map[string]string{
		"engine": "codex", "binary": cfg.CodexBin, "trace": trace,
		"wsModule": filepath.Join(adapter, "node_modules/ws/index.js"),
	})
	require.NoError(t, err)
	settingsPath := filepath.Join(cfg.WorkerHome, "codex-recorder.json")
	require.NoError(t, os.WriteFile(settingsPath, settings, 0o600))
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	wrapper := filepath.Join(cfg.WorkerHome, "codex-recorded")
	command := "#!/bin/sh\nexec " + quote(node) + " " + quote(recorder) + " " + quote(settingsPath) + " \"$@\"\n"
	require.NoError(t, os.WriteFile(wrapper, []byte(command), 0o700))
	cfg.CodexBin = wrapper
	t.Cleanup(func() {
		verifyBootstrapNativeLifecycle(t, trace)
		data, err := os.ReadFile(trace + ".errors")
		require.True(t, errors.Is(err, os.ErrNotExist) || err == nil, "读取录制器错误失败：%v", err)
		require.Empty(t, strings.TrimSpace(string(data)), "原生通信录制失败")
		connections := readBootstrapNativeTrace(t, trace)
		matched := false
		for connection, messages := range connections {
			matched = hasBootstrapNativeResult(messages, method, direction) || matched
			saveBootstrapArtifact(t, "wire", runtimeidentity.Codex, map[string]any{
				"messages": messages, "protocolErrors": []string{},
				"source": "native-upstream", "connection": connection,
			})
		}
		require.True(t, matched, "原生 %s 必须具有同连接的真实成功响应", method)
	})
}

func verifyBootstrapNativeLifecycle(t *testing.T, trace string) {
	t.Helper()
	data, err := os.ReadFile(trace + ".lifecycle.jsonl")
	require.NoError(t, err)
	var events []map[string]json.RawMessage
	started, exited := 0, 0
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
		var event map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(line, &event))
		events = append(events, event)
		var name string
		require.NoError(t, json.Unmarshal(event["event"], &name))
		switch name {
		case "native-started":
			started++
		case "native-exited":
			exited++
		}
	}
	saveBootstrapArtifact(t, "native-lifecycle", runtimeidentity.Codex, events)
	require.Positive(t, started, "必须观察到真实 CLI 启动")
	require.Equal(t, started, exited, "原生进程退出前不能宣布录制收齐")
}

func readBootstrapNativeTrace(t *testing.T, path string) map[string][]map[string]json.RawMessage {
	t.Helper()
	file, err := os.Open(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, file.Close()) }()
	connections := make(map[string][]map[string]json.RawMessage)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		var row struct {
			Engine, Connection, Direction, Delivery string
			Message                                 map[string]json.RawMessage
		}
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &row))
		require.Equal(t, "codex", row.Engine)
		require.NotEmpty(t, row.Connection, "请求 ID 只在同一连接内有效")
		require.NotEmpty(t, row.Message)
		require.NotContains(t, row.Message, "direction")
		// 未交付给 Worker 的迟到响应不能计作成功；保留缺口由正式门禁检测。
		if row.Delivery != "" {
			require.Equal(t, "native-only-client-closed", row.Delivery)
			continue
		}
		switch row.Direction {
		case "request":
			row.Message["direction"] = json.RawMessage(`"client"`)
		case "response":
			row.Message["direction"] = json.RawMessage(`"server"`)
		default:
			t.Fatalf("无效的原生报文方向：%q", row.Direction)
		}
		connections[row.Connection] = append(connections[row.Connection], row.Message)
	}
	require.NoError(t, scanner.Err())
	require.NotEmpty(t, connections, "不得以空记录代替原生通信证据")
	return connections
}

func hasBootstrapNativeResult(messages []map[string]json.RawMessage, method, direction string) bool {
	pending := make(map[string]bool)
	for _, message := range messages {
		var currentMethod, currentDirection string
		_ = json.Unmarshal(message["method"], &currentMethod)
		_ = json.Unmarshal(message["direction"], &currentDirection)
		id := string(message["id"])
		if id == "" || id == "null" {
			continue
		}
		if currentMethod != "" {
			if currentDirection == direction {
				pending[id] = currentMethod == method
			}
			continue
		}
		if currentDirection != direction && pending[id] {
			delete(pending, id)
			_, hasResult := message["result"]
			_, hasError := message["error"]
			if hasResult && !hasError {
				return true
			}
		}
	}
	return false
}
