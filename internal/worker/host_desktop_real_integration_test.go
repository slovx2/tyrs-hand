//go:build integration

package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/hostworker"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// 真实 App Server，模型、Browser MCP 和 Control 均在本机隔离模拟，不接触生产。
func TestRealHostDesktopToolsSurviveBindingChanges(t *testing.T) {
	bin := os.Getenv("TYRS_HAND_TEST_CODEX_BIN")
	if bin == "" {
		bin = "codex"
	}
	bin, err := exec.LookPath(bin)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	root, err := os.MkdirTemp("", "tyrs-host-binding-")
	require.NoError(t, err)
	defer os.RemoveAll(root)
	home, cwd, data := filepath.Join(root, "codex"), filepath.Join(root, "project"), filepath.Join(root, "data")
	for _, path := range []string{home, cwd, data} {
		require.NoError(t, os.MkdirAll(path, 0o700))
	}
	require.NoError(t, os.WriteFile(filepath.Join(cwd, "input.txt"), []byte("local file"), 0o600))
	require.NoError(t, exec.Command("git", "-C", cwd, "init").Run())
	var browserCalls atomic.Int32
	browser := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var input struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if len(input.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any
		switch input.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "isolated-browser", "version": "1.0.0"}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "browser_tabs", "description": "列出隔离浏览器标签页", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}}}}
		case "tools/call":
			browserCalls.Add(1)
			result = map[string]any{"content": []any{map[string]string{"type": "text", "text": "isolated tab"}}}
		default:
			result = map[string]any{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": input.ID, "result": result})
	}))
	defer browser.Close()
	var responseNumber atomic.Int32
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	ownerEntered := make(chan struct{}, 1)
	ownerRelease := make(chan struct{})
	responses := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if responseNumber.Load() == 0 {
			raw, _ := json.Marshal(payload["tools"])
			require.Contains(t, string(raw), `"name":"mcp__chrome"`)
			require.Contains(t, string(raw), `"name":"browser_files"`)
		}
		n := responseNumber.Add(1)
		if n == 1 {
			entered <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		if n == 3 {
			ownerEntered <- struct{}{}
			select {
			case <-ownerRelease:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		id := fmt.Sprintf("response-%d", n)
		events := []map[string]any{{"type": "response.created", "response": map[string]any{"id": id}}}
		if n%2 == 1 {
			events = append(events, map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "function_call", "call_id": fmt.Sprintf("browser-%d", n), "namespace": "mcp__chrome", "name": "browser_tabs", "arguments": "{}"}})
		} else {
			events = append(events, map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "message", "role": "assistant", "id": fmt.Sprintf("message-%d", n), "content": []any{map[string]any{"type": "output_text", "text": "浏览器正常"}}}})
		}
		if n%2 == 1 {
			events = append(events,
				map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "function_call", "call_id": fmt.Sprintf("git-%d", n), "namespace": "git", "name": "status", "arguments": "{}"}},
				map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "function_call", "call_id": fmt.Sprintf("file-%d", n), "namespace": "browser_files", "name": "stage_file", "arguments": `{"source":"input.txt"}`}},
			)
			if n == 3 {
				events = append(events, map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "function_call", "call_id": "invalid-control", "namespace": "tyrs_hand", "name": "automation_update", "arguments": `{"action":"list"}`}})
			}
		} else {
			input, _ := json.Marshal(payload["input"])
			require.Contains(t, string(input), "input.txt")
			require.Contains(t, string(input), "isolated tab")
			require.NotContains(t, string(input), "没有活动的工具授权")
			if n == 4 {
				require.Contains(t, string(input), "Workspace 绑定已失效")
			}
		}
		events = append(events, map[string]any{"type": "response.completed", "response": map[string]any{"id": id, "usage": map[string]any{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}}})
		for _, event := range events {
			raw, _ := json.Marshal(event)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], raw)
		}
	}))
	defer responses.Close()
	var bindingMu sync.Mutex
	var manifest *workerprotocol.WorkspaceManifest
	var business atomic.Int32
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/worker/v1/workspace" {
			bindingMu.Lock()
			defer bindingMu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"workspace": manifest})
			return
		}
		if strings.Contains(r.URL.Path, "desktop-turn") || strings.Contains(r.URL.Path, "/events") {
			business.Add(1)
		}
		fmt.Fprint(w, `{}`)
	}))
	defer control.Close()
	require.NoError(t, os.WriteFile(filepath.Join(home, "config.toml"), []byte(fmt.Sprintf(`model="mock-model"
approval_policy="never"
sandbox_mode="read-only"
model_provider="mock"
[model_providers.mock]
name="Isolated model"
base_url=%q
wire_api="responses"
request_max_retries=0
stream_max_retries=0
supports_websockets=false
`, responses.URL+"/v1")), 0o600))
	tokenFile := filepath.Join(root, "browser-token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("isolated-secret"), 0o600))
	cfg := config.Config{WorkerDataRoot: data, WorkerCodexHome: home, WorkerWorkspaceRoot: cwd, ControlTimeout: 3 * time.Second, HeartbeatInterval: time.Hour, TurnIdleTimeout: time.Minute, TurnMaxDuration: time.Minute, BrowserMCPURL: browser.URL, BrowserMCPTokenFile: tokenFile, BrowserFilesRoot: filepath.Join(root, "files")}
	p := NewProcessor(ctx, cfg, workerprotocol.NewClient(control.URL, "isolated", time.Second), nil, nil, zap.NewNop())
	c := NewHostDesktopController(p, nil)
	scope, err := LoadBrowserScope(data)
	require.NoError(t, err)
	tokens, err := DeriveBrowserAppServerTokens(cfg, scope)
	require.NoError(t, err)
	runtime, err := hostworker.StartRuntime(ctx, hostworker.RuntimeOptions{CodexBin: bin, CodexHome: home, Home: root, WorkspaceRoot: cwd, StateDir: data, Controller: c, BrowserWorkerToken: tokens.Worker, BrowserDesktopToken: tokens.Desktop})
	require.NoError(t, err)
	defer runtime.Close()
	p.UseHostRuntime(runtime, scope, nil)
	require.NoError(t, c.AttachRuntime(ctx, runtime))
	desktop, err := runtime.OpenEphemeralClient()
	require.NoError(t, err)
	defer desktop.Close()
	var started struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	require.NoError(t, desktop.Call(ctx, "thread/start", map[string]any{"cwd": cwd, "model": "mock-model", "approvalPolicy": "never", "sandbox": "read-only"}, &started))
	require.NotEmpty(t, started.Thread.ID)
	threadID := started.Thread.ID
	events := desktop.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	defer events.Close()
	generation := runtime.Generation()
	startTurn := func() string {
		var output struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		require.NoError(t, desktop.Call(ctx, "turn/start", map[string]any{"threadId": threadID, "input": []any{map[string]any{"type": "text", "text": "列出浏览器标签页", "textElements": []any{}}}}, &output))
		return output.Turn.ID
	}
	waitTurn := func(turnID string) {
		for {
			select {
			case event := <-events.Events():
				if event.Method == "turn/completed" {
					if matched, status := completedTurn(event.Params, threadID, turnID); matched {
						require.Equal(t, "completed", status)
						return
					}
				}
			case <-ctx.Done():
				t.Fatal("真实 App Server turn 超时")
				return
			}
		}
	}
	bind := func(owner string) {
		bindingMu.Lock()
		if owner == "" {
			manifest = nil
		} else {
			manifest = &workerprotocol.WorkspaceManifest{WorkspaceID: uuid.MustParse("11111111-1111-4111-8111-111111111111"), OwnerParticipant: &workerprotocol.ParticipantIdentity{ParticipantID: uuid.New(), DisplayName: owner}}
		}
		bindingMu.Unlock()
		require.NoError(t, c.syncHostEnvironment(ctx))
	}
	turnID := startTurn()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("模型请求未到达")
	}
	bind("负责人甲")
	c.mu.Lock()
	first := c.active[threadID]
	c.mu.Unlock()
	require.Nil(t, first.controller, "运行中的未绑定 turn 不能被重新归属")
	close(release)
	waitTurn(turnID)
	require.EqualValues(t, 1, browserCalls.Load())
	require.Zero(t, business.Load(), "绑定前开始的活动不得投影")
	journals, err := p.journals.loadAll()
	require.NoError(t, err)
	require.Empty(t, journals)
	for _, owner := range []string{"负责人甲", "负责人乙", ""} {
		bind(owner)
		require.Eventually(t, func() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.active[threadID] == nil }, time.Second, 10*time.Millisecond)
		turnID = startTurn()
		if owner == "负责人甲" {
			select {
			case <-ownerEntered:
			case <-ctx.Done():
				t.Fatal("负责人切换测试未进入 turn")
			}
			c.mu.Lock()
			running := c.active[threadID].controller
			c.mu.Unlock()
			bind("负责人乙")
			identity, ok := running.workspace.ownerParticipant()
			require.True(t, ok)
			require.Equal(t, "负责人甲", identity.DisplayName)
			require.False(t, running.controlEnabled())
			close(ownerRelease)
		}
		waitTurn(turnID)
		require.Equal(t, generation, runtime.Generation())
		persisted, err := LoadBrowserScope(data)
		require.NoError(t, err)
		require.Equal(t, scope, persisted)
	}
	require.EqualValues(t, 4, browserCalls.Load())
	require.Positive(t, business.Load())
	require.Eventually(t, func() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.active[threadID] == nil }, time.Second, 10*time.Millisecond)
	// 重启后正常 resume 保持同一官方 Thread，并重新注入 MCP。
	require.NoError(t, runtime.Restart())
	restored, err := runtime.OpenEphemeralClient()
	require.NoError(t, err)
	defer restored.Close()
	desktop = restored
	events = desktop.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	defer events.Close()
	require.NoError(t, desktop.Call(ctx, "thread/resume", map[string]any{"threadId": threadID}, &started))
	require.Equal(t, threadID, started.Thread.ID)
	turnID = startTurn()
	waitTurn(turnID)
	require.EqualValues(t, 5, browserCalls.Load())
	persisted, err := LoadBrowserScope(data)
	require.NoError(t, err)
	require.Equal(t, scope, persisted)
	require.NotEqual(t, generation, runtime.Generation())
}
