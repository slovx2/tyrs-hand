//go:build integration

package bootstrap

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// 原生 request_permissions → 真 SSH/Hub/Control → Discord 回答 → 原生命令及文件。
func TestWorkerControlPermissionsRealSSH(t *testing.T) {
	requireControlNetworkIsolation(t)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Second)
	defer cancel()
	scenario := &nativePermissionScenario{}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { scenario.serve(t, w, req) }))
	t.Cleanup(model.Close)
	ready := make(chan struct{})
	close(ready)
	f := newControlRuntimeFixture(t, ctx, model.URL, ready)
	configPath := filepath.Join(f.cfg.WorkerCodexHome, "config.toml")
	configFile, err := os.OpenFile(configPath, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = configFile.WriteString("\n[features]\nrequest_permissions_tool=true\nexec_permission_approvals=true\n")
	require.NoError(t, err)
	require.NoError(t, configFile.Close())
	// 真实权限路径在工作区外，并从默认 /tmp 可写范围中排除。
	home, err := filepath.EvalSymlinks(f.cfg.WorkerHome)
	require.NoError(t, err)
	scenario.path = filepath.Join(home, "permission-effect.txt")
	discord := startControlDiscordFixture(t, ctx, f.db)
	workerCtx, cancelWorker := context.WithCancel(ctx)
	app, cleanup, err := InitializeWorker(workerCtx, f.cfg)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- app.Run(workerCtx) }()
	var stop sync.Once
	t.Cleanup(func() { stop.Do(func() { cancelWorker(); <-done; cleanup() }) })
	entry, err := app.Runtimes.Entry(runtimeidentity.Codex)
	require.NoError(t, err)
	received := make(chan codex.ServerRequest, 8)
	client, _ := connectBootstrapSSHWithOptions(t, ctx, entry, f.signer, codex.SocketClientOptions{
		ServerRequestHandler: func(ctx context.Context, request codex.ServerRequest) (any, error) {
			received <- request
			if request.Method == interactiveprotocol.PermissionApproval {
				// 较早的 Desktop 答案故意扩权，Hub 不得让它赢过合法 Discord 答案。
				return map[string]any{"permissions": map[string]any{"network": map[string]any{"enabled": true}}, "scope": "session"}, nil
			}
			return map[string]string{"decision": "decline"}, nil
		},
	})
	var started struct{ Thread struct{ ID string } }
	require.NoError(t, client.Call(ctx, "thread/start", map[string]any{
		"cwd": f.cfg.WorkerWorkspaceRoot, "approvalPolicy": "on-request", "sandbox": "workspace-write",
	}, &started))
	thread := started.Thread.ID
	events := client.Subscribe(codex.ThreadFilter{ThreadID: thread})
	t.Cleanup(events.Close)
	startTurn := func(prompt string) {
		require.NoError(t, client.Call(ctx, "turn/start", map[string]any{"threadId": thread,
			"sandboxPolicy": map[string]any{"type": "workspaceWrite", "writableRoots": []string{},
				"networkAccess": false, "excludeTmpdirEnvVar": true, "excludeSlashTmp": true},
			"input": []map[string]any{{"type": "text", "text": prompt, "text_elements": []any{}}},
		}, nil))
	}
	startTurn("PERMISSION_CONTROL_SETUP")
	awaitBootstrapTurn(t, ctx, events)
	awaitControlRunCount(t, ctx, f.db, runtimeidentity.Codex, 1)
	manager := discordintegration.NewManager(f.db, nil)
	var expected string
	var evidence []map[string]any
	for index, test := range []nativePermissionCase{
		{id: "PERMISSION_TURN_ALLOW", scope: "turn", allowed: true},
		{id: "PERMISSION_TURN_DENY", scope: "turn", allowed: false},
		{id: "PERMISSION_SESSION_ALLOW", scope: "session", allowed: true},
		{id: "PERMISSION_SESSION_REUSE", executeOnly: true, allowed: true},
	} {
		active := test
		scenario.mu.Lock()
		scenario.active = &active
		scenario.mu.Unlock()
		startTurn(active.id)
		var controlID uuid.UUID
		var nativeParams, nativeAnswer json.RawMessage
		if !active.executeOnly {
			var request codex.ServerRequest
			select {
			case request = <-received:
			case <-time.After(15 * time.Second):
				t.Fatal("真实 Codex 没有发出权限审批")
			}
			require.Equal(t, interactiveprotocol.PermissionApproval, request.Method)
			nativeParams = request.Params
			discord.deliverUntil(t, ctx, func() bool {
				return f.db.QueryRowContext(ctx, "SELECT q.id FROM codex_interactive_requests q JOIN codex_thread_controls c ON c.id=q.control_id WHERE c.engine='codex' AND q.thread_id=$1 AND q.app_server_request_id=$2::jsonb AND q.status='pending' AND q.discord_message_id IS NOT NULL", thread, request.ID).Scan(&controlID) == nil
			})
			if expected == "" {
				require.NoFileExists(t, scenario.path)
			} else {
				data, readErr := os.ReadFile(scenario.path)
				require.NoError(t, readErr)
				require.Equal(t, expected, string(data))
			}
			option := 0
			if !active.allowed {
				option = 1
			} else if active.scope == "session" {
				option = 2
			}
			answer, answerErr := manager.AnswerInteractive(ctx, "protocol", controlID, 0, option, "")
			require.NoError(t, answerErr)
			require.True(t, answer.Complete)
			// 已决议后的冲突答案不能改变持久化赢家。
			_, answerErr = manager.AnswerInteractive(ctx, "protocol", controlID, 0, 1, "")
			require.NoError(t, answerErr)
			require.NoError(t, f.db.QueryRowContext(ctx, "SELECT answer FROM codex_interactive_requests WHERE id=$1 AND status='resolved'", controlID).Scan(&nativeAnswer))
		}
		awaitBootstrapTurn(t, ctx, events)
		awaitControlRunCount(t, ctx, f.db, runtimeidentity.Codex, 2+index)
		if active.allowed {
			expected += active.id + "\n"
		}
		content, readErr := os.ReadFile(scenario.path)
		require.NoError(t, readErr)
		require.Equal(t, expected, string(content), "权限失效/拒绝后不能写入；授权命令只执行一次")
		scenario.mu.Lock()
		require.True(t, active.commandSeen, "原生 exec_command 结果必须回到模型")
		if !active.executeOnly {
			require.True(t, active.permissionSeen, "真实权限审批结果必须回到模型")
		}
		nativeReturned := active.commandSeen
		scenario.mu.Unlock()
		evidence = append(evidence, map[string]any{"case": active.id, "scope": active.scope,
			"allowed": active.allowed, "nativeResultReturned": nativeReturned, "controlRequestId": controlID,
			"method": interactiveprotocol.PermissionApproval, "nativeParams": nativeParams, "nativeAnswer": nativeAnswer})
	}
	// 已有 session 授权只覆盖旧文件；新路径必须产生真实的 pending 请求。
	restartPath := filepath.Join(home, "permission-restart-forbidden.txt")
	scenario.mu.Lock()
	scenario.path = restartPath
	scenario.active = &nativePermissionCase{id: "PERMISSION_RESTART", scope: "turn"}
	scenario.mu.Unlock()
	startTurn("PERMISSION_RESTART")
	var pending codex.ServerRequest
	select {
	case pending = <-received:
	case <-time.After(15 * time.Second):
		t.Fatal("重启场景没有原生权限请求")
	}
	require.Equal(t, interactiveprotocol.PermissionApproval, pending.Method)
	var restartID uuid.UUID
	discord.deliverUntil(t, ctx, func() bool {
		return f.db.QueryRowContext(ctx, "SELECT id FROM codex_interactive_requests WHERE thread_id=$1 AND app_server_request_id=$2::jsonb AND status='pending'", thread, pending.ID).Scan(&restartID) == nil
	})
	restart := verifyControlInteractionRestart(t, ctx, f, app, runtimeidentity.Codex, restartID, pending, json.RawMessage(`{"permissions":{},"scope":"turn"}`))
	require.NoFileExists(t, restartPath, "旧回答不得造成文件副作用")
	var claudeInteractions int
	require.NoError(t, f.db.QueryRowContext(ctx, "SELECT count(*) FROM codex_interactive_requests q JOIN codex_thread_controls c ON c.id=q.control_id WHERE c.engine='claude-code'").Scan(&claudeInteractions))
	require.Zero(t, claudeInteractions)
	saveBootstrapArtifact(t, "permission-effects", runtimeidentity.Codex, map[string]any{"cases": evidence, "content": expected, "restart": restart})
}
