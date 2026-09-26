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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/slovx2/tyrs-hand/internal/interactiveprotocol"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
)

// 原生 Claude CLI 启动真实 MCP Server，唯一替身为本机模型与 Discord HTTP。
func TestWorkerControlMcpRealSSH(t *testing.T) {
	requireControlNetworkIsolation(t)
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	var mu sync.Mutex
	var activeID, action string
	var sent, resultSeen, restarting bool
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.Contains(req.URL.Path, "count_tokens") {
			_, _ = io.WriteString(w, "{\"input_tokens\":10}")
			return
		}
		if req.URL.Path == "/api/hello" {
			_, _ = io.WriteString(w, "{}")
			return
		}
		if req.URL.Path != "/v1/messages" {
			http.NotFound(w, req)
			return
		}
		body, err := io.ReadAll(io.LimitReader(req.Body, 8<<20))
		if err != nil {
			t.Error(err)
			return
		}
		var payload controlAutomationPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if !strings.Contains(string(body), activeID) || len(payload.Tools) == 0 {
			// Control 的后台标题请求同样经过真实 SDK，但不能消费业务场景的工具回合。
			bootstrapModelText(w, true)
			return
		}
		if !sent {
			found := false
			for _, tool := range payload.Tools {
				found = found || tool.Name == "mcp__fixture__confirm_fixture"
			}
			if !found {
				t.Error("真实 SDK 没有声明 MCP fixture 工具")
			}
			sent = true
			automationToolResponse(w, activeID, "mcp__fixture__confirm_fixture", map[string]any{})
			return
		}
		result, found, failed := automationToolResult(payload, activeID)
		if !found {
			bootstrapModelText(w, true)
			return
		}
		if restarting {
			bootstrapModelText(w, true)
			return
		}
		resultSeen = found && !failed && strings.Contains(string(result), "MCP_ACTION_"+action)
		if !resultSeen {
			t.Error("原生 MCP 决策未正确回到真实模型请求")
		}
		bootstrapModelText(w, true)
	}))
	t.Cleanup(model.Close)
	ready := make(chan struct{})
	close(ready)
	f := newControlRuntimeFixture(t, ctx, model.URL, ready)
	discord := startControlDiscordFixture(t, ctx, f)
	workerCtx, cancelWorker := context.WithCancel(ctx)
	app, cleanup, err := InitializeWorker(workerCtx, f.cfg)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- app.Run(workerCtx) }()
	t.Cleanup(func() { cancelWorker(); <-done; cleanup() })
	entry, err := app.Runtimes.Entry(runtimeidentity.Claude)
	require.NoError(t, err)
	received := make(chan codex.ServerRequest, 8)
	client, _ := connectBootstrapSSHWithOptions(t, ctx, entry, f.signer, codex.SocketClientOptions{
		ServerRequestHandler: func(_ context.Context, request codex.ServerRequest) (any, error) {
			received <- request
			// 这个较早到达的答案不满足表单类型或 URL 内容约束，不能抢占有效决策。
			return map[string]any{"action": "accept", "content": map[string]any{"value": 42}, "_meta": nil}, nil
		},
	})
	node, err := exec.LookPath("node")
	require.NoError(t, err)
	fixture := filepath.Join(filepath.Dir(filepath.Dir(os.Getenv("TYRS_HAND_TEST_CLAUDE_BIN"))), "test", "fixtures", "mcp-interactive-server.mjs")
	manager := discordintegration.NewManager(f.db, nil)
	var evidence []map[string]any
	for index, test := range []struct {
		mode, action string
		restart      bool
	}{{"form", "accept", false}, {"form", "decline", false}, {"form", "cancel", false},
		{"url", "accept", false}, {"url", "decline", false}, {"form", "cancel", true}} {
		path := filepath.Join(f.cfg.WorkerWorkspaceRoot, fmt.Sprintf("mcp-control-%d.txt", index))
		mu.Lock()
		activeID = fmt.Sprintf("toolu_mcp_control_%d", index)
		action = test.action
		sent = false
		resultSeen = false
		restarting = test.restart
		mu.Unlock()
		var started struct{ Thread struct{ ID string } }
		require.NoError(t, client.Call(ctx, "thread/start", map[string]any{
			"cwd": f.cfg.WorkerWorkspaceRoot, "approvalPolicy": "never", "sandbox": "danger-full-access",
			"config": map[string]any{"mcp_servers": map[string]any{"fixture": map[string]any{
				"command": node, "args": []string{fixture}, "env": map[string]string{"FIXTURE_EFFECT_PATH": path, "FIXTURE_ELICITATION_MODE": test.mode},
			}}},
		}, &started))
		thread := started.Thread.ID
		events := client.Subscribe(codex.ThreadFilter{ThreadID: thread})
		t.Cleanup(events.Close)
		require.NoError(t, client.Call(ctx, "turn/start", map[string]any{"threadId": thread, "input": []map[string]any{{"type": "text", "text": activeID}}}, nil))
		var request codex.ServerRequest
		select {
		case request = <-received:
		case <-time.After(15 * time.Second):
			t.Fatal("真实 SDK 没有发出 MCP 交互")
		}
		require.Equal(t, interactiveprotocol.MCPElicitation, request.Method)
		var original map[string]any
		require.NoError(t, json.Unmarshal(request.Params, &original))
		require.Equal(t, test.mode, original["mode"])
		require.NotContains(t, original, "itemId", "不能给 MCP 伪造条目标识")
		var controlID uuid.UUID
		discord.deliverUntil(t, ctx, func() bool {
			return f.db.QueryRowContext(ctx, "SELECT q.id FROM codex_interactive_requests q JOIN codex_thread_controls c ON c.id=q.control_id WHERE c.engine='claude-code' AND q.thread_id=$1 AND q.app_server_request_id=$2::jsonb AND q.item_id='' AND q.status='pending' AND q.discord_message_id IS NOT NULL", thread, request.ID).Scan(&controlID) == nil
		})
		require.NoFileExists(t, path)
		if test.restart {
			restart := verifyControlInteractionRestart(t, ctx, f, app, runtimeidentity.Claude, controlID, request, json.RawMessage(`{"action":"cancel","content":null,"_meta":null}`))
			require.NoFileExists(t, path, "重启后的迟到答案不得产生 MCP 文件副作用")
			evidence = append(evidence, restart)
			continue
		}
		option := map[string]int{"accept": 0, "decline": 1, "cancel": 2}[test.action]
		answered, err := manager.AnswerInteractive(ctx, f.guildID, controlID, 0, option, "")
		require.NoError(t, err)
		value := fmt.Sprintf("DISCORD_MCP_%d", index)
		if test.action == "accept" && test.mode == "form" {
			require.False(t, answered.Complete)
			answered, err = manager.AnswerInteractive(ctx, f.guildID, controlID, 1, -1, value)
			require.NoError(t, err)
		}
		require.True(t, answered.Complete, "拒绝和取消不应继续索取表单字段")
		var nativeAnswer json.RawMessage
		require.NoError(t, f.db.QueryRowContext(ctx, "SELECT answer FROM codex_interactive_requests WHERE id=$1 AND status='resolved'", controlID).Scan(&nativeAnswer))
		awaitBootstrapTurn(t, ctx, events)
		awaitControlRunCount(t, ctx, f, runtimeidentity.Claude, index+1)
		if test.action == "accept" {
			if test.mode == "url" {
				value = "URL_CONFIRMED"
			}
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, value+"\n", string(data))
		} else {
			require.NoFileExists(t, path)
		}
		mu.Lock()
		require.True(t, resultSeen)
		mu.Unlock()
		evidence = append(evidence, map[string]any{"mode": test.mode, "action": test.action, "controlRequestId": controlID, "actualFileCreated": test.action == "accept",
			"method": interactiveprotocol.MCPElicitation, "nativeParams": request.Params, "nativeAnswer": nativeAnswer})
	}
	var codexInteractions int
	require.NoError(t, f.db.QueryRowContext(ctx, "SELECT count(*) FROM codex_interactive_requests q JOIN codex_thread_controls c ON c.id=q.control_id WHERE c.worker_id=$1 AND c.engine='codex'", f.workerID).Scan(&codexInteractions))
	require.Zero(t, codexInteractions)
	saveBootstrapArtifact(t, "mcp-effects", runtimeidentity.Claude, map[string]any{"cases": evidence})
}
