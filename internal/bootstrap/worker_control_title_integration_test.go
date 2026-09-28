//go:build integration

package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
)

// 标题经真实 Control 调度与官方 CLI 生成；探测原生工具注册表，不能只检查配置字面值。
func TestWorkerControlCodexTitleIsolationRealSSH(t *testing.T) {
	requireControlNetworkIsolation(t)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	var mu sync.Mutex
	var titleRequests int
	var toolOutput json.RawMessage
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		require.NoError(t, err)
		var request struct {
			Model string
			Input []struct {
				Type   string
				CallID string `json:"call_id"`
				Output json.RawMessage
			}
			Text struct{ Format struct{ Type string } }
		}
		require.NoError(t, json.Unmarshal(body, &request))
		if request.Text.Format.Type != "json_schema" {
			require.Contains(t, string(body), "confirm_mobile", "普通会话必须保留已配置 MCP 工具")
			bootstrapModelText(w, false)
			return
		}
		require.Equal(t, "gpt-5.6-luna", request.Model)
		mu.Lock()
		defer mu.Unlock()
		titleRequests++
		require.LessOrEqual(t, titleRequests, 2, "标题辅助任务不得重放")
		for _, input := range request.Input {
			if input.Type == "custom_tool_call_output" && input.CallID == "title-catalog" {
				toolOutput = append(json.RawMessage(nil), input.Output...)
			}
		}
		bootstrapEvent(w, "response.created", map[string]any{"response": map[string]any{"id": "title-isolation"}})
		if len(toolOutput) == 0 {
			// 官方模型保留隔离的 JS 编排入口；其内部必须没有宿主工具。
			bootstrapEvent(w, "response.output_item.done", map[string]any{"item": map[string]any{
				"type": "custom_tool_call", "namespace": "functions", "name": "exec",
				"id": "title-catalog-item", "call_id": "title-catalog",
				"input": `text({marker: "TITLE_TOOL_REGISTRY", names: ALL_TOOLS.map(tool => tool.name).sort()})`,
			}})
		} else {
			bootstrapEvent(w, "response.output_item.done", map[string]any{"item": map[string]any{
				"type": "message", "role": "assistant", "id": "title-isolation-message",
				"content": []map[string]string{{"type": "output_text", "text": `{"title":"隔离标题验收"}`}},
			}})
		}
		bootstrapEvent(w, "response.completed", map[string]any{"response": map[string]any{"id": "title-isolation"}})
	}))
	t.Cleanup(model.Close)
	ready := make(chan struct{})
	close(ready)
	f := newControlRuntimeFixture(t, ctx, model.URL, ready)
	recordBootstrapCodexUpstream(t, &f.cfg, "config/read", "client")
	// 持久配置中已有真实 SDK MCP；空对象的深合并不能移除它。
	node, err := exec.LookPath("node")
	require.NoError(t, err)
	_, source, _, ok := runtime.Caller(0)
	require.True(t, ok)
	fixture := filepath.Join(filepath.Dir(source), "../../tools/mobile-e2e/fixtures/mcp-server.mjs")
	adapter := filepath.Dir(filepath.Dir(os.Getenv("TYRS_HAND_TEST_CLAUDE_BIN")))
	configPath := filepath.Join(f.cfg.WorkerCodexHome, "config.toml")
	config, err := os.ReadFile(configPath)
	require.NoError(t, err)
	config = append(config, []byte(fmt.Sprintf("\n[mcp_servers.\"title.fixture\"]\ncommand=%q\nargs=[%q,%q,%q]\n",
		node, fixture, adapter, f.cfg.WorkerWorkspaceRoot))...)
	require.NoError(t, os.WriteFile(configPath, config, 0o600))
	discord := startControlDiscordFixture(t, ctx, f)
	workerCtx, stopWorker := context.WithCancel(ctx)
	app, cleanup, err := InitializeWorker(workerCtx, f.cfg)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- app.Run(workerCtx) }()
	t.Cleanup(func() { stopWorker(); <-done; cleanup() })
	entry, err := app.Runtimes.Entry(runtimeidentity.Codex)
	require.NoError(t, err)
	client, _ := connectBootstrapSSH(t, ctx, entry, f.signer)
	var started struct{ Thread struct{ ID string } }
	require.NoError(t, client.Call(ctx, "thread/start", map[string]any{
		"cwd": f.cfg.WorkerWorkspaceRoot, "approvalPolicy": "never", "sandbox": "read-only",
	}, &started))
	var turn struct{ Turn struct{ ID string } }
	require.NoError(t, client.Call(ctx, "turn/start", map[string]any{
		"threadId": started.Thread.ID, "input": []map[string]string{{"type": "text", "text": "TITLE_ISOLATION_ACCEPTANCE"}},
	}, &turn))
	awaitControlRunCount(t, ctx, f, runtimeidentity.Codex, 1)
	discord.deliverUntil(t, ctx, func() bool {
		var title, titleSource string
		err := f.db.QueryRowContext(ctx, `SELECT s.title,s.title_source FROM workspace_sessions s
			JOIN codex_thread_controls c ON c.session_id=s.id
			WHERE c.worker_id=$1 AND c.external_thread_id=$2`, f.workerID, started.Thread.ID).Scan(&title, &titleSource)
		return err == nil && title == "隔离标题验收" && titleSource == "generated"
	})
	mu.Lock()
	defer mu.Unlock()
	t.Logf("原生标题工具注册表结果: %s", toolOutput)
	require.Equal(t, 2, titleRequests)
	require.Contains(t, string(toolOutput), "TITLE_TOOL_REGISTRY")
	var parts []struct{ Text string }
	require.NoError(t, json.Unmarshal(toolOutput, &parts))
	var output strings.Builder
	for _, part := range parts {
		output.WriteString(part.Text)
	}
	var result struct {
		Marker string
		Names  []string
	}
	text := output.String()
	start := strings.Index(text, `{"marker":"TITLE_TOOL_REGISTRY"`)
	require.GreaterOrEqual(t, start, 0, "必须得到原生 JS 执行器的真实结果")
	require.NoError(t, json.NewDecoder(strings.NewReader(text[start:])).Decode(&result))
	require.Empty(t, result.Names, "标题不得访问命令、文件、MCP、网络或其他宿主工具")
	unchanged, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Equal(t, config, unchanged, "仅覆盖辅助会话，不能修改宿主配置")
	discord.deliverUntil(t, ctx, func() bool {
		var pending int
		err := f.db.QueryRowContext(ctx, `SELECT count(*) FROM integration_outbox WHERE status<>'completed'`).Scan(&pending)
		return err == nil && pending == 0
	})
}
