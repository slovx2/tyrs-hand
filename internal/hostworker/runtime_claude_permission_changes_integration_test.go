//go:build integration

package hostworker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

// 只脚本化模型的工具意图；文件写入、边界拒绝、审批与网络隔离均由真实适配器、SDK/CLI 和 OS 沙箱产生。
type runtimeClaudePermissionChangesFixture struct {
	project  string
	outside  string
	modelURL string
	calls    atomic.Int64
	mu       sync.Mutex
	steps    map[string]int
}

type permissionToolResult struct {
	seen    bool
	isError bool
	text    string
}

func lastPermissionToolResult(t *testing.T, body []byte) permissionToolResult {
	var request struct {
		Messages []struct {
			Role    string
			Content json.RawMessage
		}
	}
	require.NoError(t, json.Unmarshal(body, &request))
	// 固定 CLI 会在工具结果后追加 system 消息，因此取最近一条携带工具结果的 user 消息。
	for index := len(request.Messages) - 1; index >= 0; index-- {
		message := request.Messages[index]
		if message.Role == "assistant" {
			break
		}
		var blocks []struct {
			Type    string          `json:"type"`
			IsError bool            `json:"is_error"`
			Content json.RawMessage `json:"content"`
		}
		if message.Role != "user" || json.Unmarshal(message.Content, &blocks) != nil {
			continue
		}
		for _, block := range blocks {
			if block.Type != "tool_result" {
				continue
			}
			text := ""
			if json.Unmarshal(block.Content, &text) != nil {
				var parts []struct{ Text string }
				_ = json.Unmarshal(block.Content, &parts)
				for _, part := range parts {
					text += part.Text
				}
			}
			return permissionToolResult{seen: true, isError: block.IsError, text: text}
		}
	}
	return permissionToolResult{}
}

func (f *runtimeClaudePermissionChangesFixture) model(t *testing.T, w http.ResponseWriter, request *http.Request, body []byte) {
	f.calls.Add(1)
	marker := ""
	for _, candidate := range []string{"PERM001_WORKSPACE", "PERM001_READONLY", "PERM001_ASK", "PERM001_NETWORK_OFF", "PERM001_NETWORK_ON"} {
		if strings.Contains(string(body), candidate) {
			marker = candidate
		}
	}
	require.NotEmpty(t, marker, "只接受已脚本化的权限回合")
	f.mu.Lock()
	step := f.steps[marker]
	f.steps[marker] = step + 1
	f.mu.Unlock()
	result := lastPermissionToolResult(t, body)
	id := fmt.Sprintf("%s-%d", strings.ToLower(marker), step)
	write := func(name, content string) {
		runtimeClaudeToolModel(t, w, id, "Write", map[string]any{"file_path": name, "content": content})
	}
	probe := func() {
		// 仅访问本轮回环 Mock；是否可达由真实 OS 网络沙箱决定。
		script := fmt.Sprintf("require('node:http').get(%q,()=>{console.log('NET_OK');process.exit(0)})"+
			".on('error',()=>{console.log('NET_BLOCKED');process.exit(0)});setTimeout(()=>{console.log('NET_BLOCKED');process.exit(0)},3000)",
			f.modelURL+"/permission-probe")
		runtimeClaudeToolModel(t, w, id, "Bash", map[string]any{"command": "node -e " + shellQuote(script)})
	}
	done := func() { runtimeTextModel(w, request, "PERM001_DONE", id) }
	switch marker + fmt.Sprint(step) {
	case "PERM001_WORKSPACE0":
		write(filepath.Join(f.project, "inside.txt"), "inside")
	case "PERM001_WORKSPACE1":
		require.True(t, result.seen)
		require.False(t, result.isError, "工作区内写入必须成功: %s", result.text)
		write(f.outside, "forbidden")
	case "PERM001_WORKSPACE2":
		require.True(t, result.seen && result.isError, "工作区外写入必须被真实拒绝: %s", result.text)
		done()
	case "PERM001_READONLY0":
		write(filepath.Join(f.project, "readonly.txt"), "forbidden")
	case "PERM001_READONLY1":
		require.True(t, result.seen && result.isError, "切换为只读后写入必须被拒绝: %s", result.text)
		done()
	case "PERM001_ASK0":
		write(filepath.Join(f.project, "ask-declined.txt"), "forbidden")
	case "PERM001_ASK1":
		require.True(t, result.seen && result.isError, "用户拒绝后写入不能执行: %s", result.text)
		write(filepath.Join(f.project, "ask-accepted.txt"), "accepted")
	case "PERM001_ASK2":
		require.True(t, result.seen)
		require.False(t, result.isError, "用户允许后写入必须成功: %s", result.text)
		done()
	case "PERM001_NETWORK_OFF0", "PERM001_NETWORK_ON0":
		probe()
	case "PERM001_NETWORK_OFF1":
		require.Contains(t, result.text, "NET_BLOCKED", "禁网策略必须在 OS 层阻断")
		require.NotContains(t, result.text, "NET_OK")
		done()
	case "PERM001_NETWORK_ON1":
		require.Contains(t, result.text, "NET_OK", "开放网络后同一命令必须可达")
		done()
	default:
		t.Errorf("未脚本化的权限模型请求 %s#%d", marker, step)
		http.Error(w, "unexpected model request", http.StatusBadRequest)
	}
}

func runtimeClaudeToolModel(t *testing.T, w http.ResponseWriter, id, tool string, input map[string]any) {
	w.Header().Set("Content-Type", "text/event-stream")
	event := func(kind string, value map[string]any) {
		value["type"] = kind
		encoded, err := json.Marshal(value)
		require.NoError(t, err)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, encoded)
	}
	encoded, err := json.Marshal(input)
	require.NoError(t, err)
	event("message_start", map[string]any{"message": map[string]any{"id": "msg_" + id, "type": "message", "role": "assistant",
		"model": "claude-config-model", "content": []any{}, "stop_reason": nil, "usage": map[string]int{"input_tokens": 10, "output_tokens": 1}}})
	event("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "tool_use", "id": "toolu_" + id, "name": tool, "input": map[string]any{}}})
	event("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(encoded)}})
	event("content_block_stop", map[string]any{"index": 0})
	event("message_delta", map[string]any{"delta": map[string]any{"stop_reason": "tool_use"}, "usage": map[string]int{"output_tokens": 10}})
	event("message_stop", map[string]any{})
}

// PERMISSION-001：同一会话逐回合变更沙箱与审批策略，真实文件写入、工作区边界、审批和网络立即按新策略生效。
func verifyRuntimeClaudePermissionChanges(t *testing.T, ctx context.Context, connection *ssh.Client, fixture *runtimeClaudePermissionChangesFixture, modelURL string) {
	t.Helper()
	fixture.modelURL = modelURL
	require.NoError(t, os.MkdirAll(fixture.project, 0o755))
	approvals := make(chan codex.ServerRequest, 4)
	decisions := make(chan string, 4)
	client, _ := connectRuntimeSSHWithTrace(t, ctx, connection, runtimeidentity.Claude, codex.SocketClientOptions{
		ServerRequestHandler: func(_ context.Context, request codex.ServerRequest) (any, error) {
			approvals <- request
			return map[string]string{"decision": <-decisions}, nil
		},
	})
	thread := readSessionThread(t, ctx, client, "thread/start", map[string]any{
		"cwd": fixture.project, "approvalPolicy": "never", "sandbox": "workspace-write"})
	workspace := func(network bool) map[string]any {
		// 显式排除 /tmp，否则临时根下的“工作区外”路径会被默认可写目录放行。
		return map[string]any{"type": "workspaceWrite", "writableRoots": []string{fixture.project},
			"excludeSlashTmp": true, "excludeTmpdirEnvVar": true, "networkAccess": network}
	}
	run := func(text, approval string, sandbox map[string]any) {
		var result struct{ Turn struct{ ID string } }
		require.NoError(t, client.Call(ctx, "turn/start", map[string]any{"threadId": thread.ID,
			"input":          []map[string]string{{"type": "text", "text": text}},
			"approvalPolicy": approval, "sandboxPolicy": sandbox}, &result))
		waitSessionTurn(t, ctx, client, thread.ID, result.Turn.ID)
	}
	run("PERM001_WORKSPACE", "never", workspace(false))
	run("PERM001_READONLY", "never", map[string]any{"type": "readOnly"})
	decisions <- "decline"
	decisions <- "accept"
	events := client.Subscribe(codex.ThreadFilter{ThreadID: thread.ID})
	run("PERM001_ASK", "on-request", workspace(false))
	events.Close()
	// 文件审批参数只携带 itemId；经同回合真实 fileChange 条目确认审批对应的待写文件。
	changes := map[string]string{}
	for event := range events.Events() {
		var params struct {
			Item struct {
				ID, Type string
				Changes  []struct{ Path string }
			}
		}
		if json.Unmarshal(event.Params, &params) != nil || params.Item.Type != "fileChange" {
			continue
		}
		for _, change := range params.Item.Changes {
			changes[params.Item.ID] = change.Path
		}
	}
	for _, want := range []string{"ask-declined.txt", "ask-accepted.txt"} {
		select {
		case request := <-approvals:
			require.Equal(t, "item/fileChange/requestApproval", request.Method)
			var approval struct{ ItemID string }
			require.NoError(t, json.Unmarshal(request.Params, &approval))
			require.Equal(t, filepath.Join(fixture.project, want), changes[approval.ItemID], "审批必须对应真实待写文件")
		default:
			t.Fatalf("写入 %s 前未收到真实审批", want)
		}
	}
	run("PERM001_NETWORK_OFF", "never", workspace(false))
	run("PERM001_NETWORK_ON", "never", workspace(true))
	select {
	case extra := <-approvals:
		t.Fatalf("不审批策略下仍发起审批: %s", extra.Method)
	default:
	}
	read := func(name string) (string, bool) {
		data, err := os.ReadFile(name)
		if os.IsNotExist(err) {
			return "", false
		}
		require.NoError(t, err)
		return string(data), true
	}
	content, ok := read(filepath.Join(fixture.project, "inside.txt"))
	require.True(t, ok)
	require.Equal(t, "inside", content)
	content, ok = read(filepath.Join(fixture.project, "ask-accepted.txt"))
	require.True(t, ok)
	require.Equal(t, "accepted", content, "允许后只写入一次")
	for _, name := range []string{fixture.outside, filepath.Join(fixture.project, "readonly.txt"),
		filepath.Join(fixture.project, "ask-declined.txt")} {
		_, ok := read(name)
		require.False(t, ok, "被拒绝的写入不能落盘: %s", name)
	}
	require.Equal(t, int64(12), fixture.calls.Load(), "每个工具结果只回模一次，拒绝后不重放")
}
