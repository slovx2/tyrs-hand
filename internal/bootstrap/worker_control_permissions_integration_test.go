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
	testWorkerControlPermissionsRealSSH(t, runtimeidentity.Codex)
}

func TestWorkerControlClaudePermissionsRealSSH(t *testing.T) {
	testWorkerControlPermissionsRealSSH(t, runtimeidentity.Claude)
}

func testWorkerControlPermissionsRealSSH(t *testing.T, engine runtimeidentity.Engine) {
	requireControlNetworkIsolation(t)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Second)
	defer cancel()
	scenario := &nativePermissionScenario{}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if engine == runtimeidentity.Claude {
			scenario.serveClaude(t, w, req)
		} else {
			scenario.serve(t, w, req)
		}
	}))
	t.Cleanup(model.Close)
	ready := make(chan struct{})
	close(ready)
	f := newControlRuntimeFixture(t, ctx, model.URL, ready)
	if engine == runtimeidentity.Codex {
		configPath := filepath.Join(f.cfg.WorkerCodexHome, "config.toml")
		configFile, err := os.OpenFile(configPath, os.O_APPEND|os.O_WRONLY, 0o600)
		require.NoError(t, err)
		_, err = configFile.WriteString("\n[features]\nrequest_permissions_tool=true\nexec_permission_approvals=true\n")
		require.NoError(t, err)
		require.NoError(t, configFile.Close())
	}
	// 真实权限路径在工作区外，并从默认 /tmp 可写范围中排除。
	home, err := filepath.EvalSymlinks(f.cfg.WorkerHome)
	require.NoError(t, err)
	// 固定 CLI 的 Linux 沙箱按目录根挂载；原提案明确请求独立目录，绝不将单文件答案扩成父目录。
	scenario.grantRoot = filepath.Join(home, "permission-scope")
	require.NoError(t, os.Mkdir(scenario.grantRoot, 0o700))
	scenario.path = filepath.Join(scenario.grantRoot, "permission-effect.txt")
	discord := startControlDiscordFixture(t, ctx, f)
	workerCtx, cancelWorker := context.WithCancel(ctx)
	app, cleanup, err := InitializeWorker(workerCtx, f.cfg)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- app.Run(workerCtx) }()
	var stop sync.Once
	t.Cleanup(func() { stop.Do(func() { cancelWorker(); <-done; cleanup() }) })
	entry, err := app.Runtimes.Entry(engine)
	require.NoError(t, err)
	received := make(chan codex.ServerRequest, 8)
	client, _, trace := connectBootstrapSSHWithTrace(t, ctx, entry, f.signer, codex.SocketClientOptions{
		ServerRequestHandler: func(ctx context.Context, request codex.ServerRequest) (any, error) {
			if request.Method == interactiveprotocol.PermissionApproval {
				received <- request
				// 较早的 Desktop 答案故意扩权，Hub 不得让它赢过合法 Discord 答案。
				return map[string]any{"permissions": map[string]any{"network": map[string]any{"enabled": true}}, "scope": "session"}, nil
			}
			if engine == runtimeidentity.Claude && request.Method == interactiveprotocol.FileApproval {
				// 文件操作审批不扩大沙箱；权限拒绝/到期仍必须阻止外部 Write。
				return map[string]string{"decision": "accept"}, nil
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
	awaitControlRunCount(t, ctx, f, engine, 1)
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
		if engine == runtimeidentity.Claude {
			// Write 使用独立新文件，避免将既有文件的 Read 前置条件混入权限语义。
			scenario.path = filepath.Join(scenario.grantRoot, active.id+".txt")
		}
		scenario.mu.Unlock()
		startTurn(active.id)
		var controlID uuid.UUID
		var nativeParams, nativeAnswer json.RawMessage
		if !active.executeOnly {
			var request codex.ServerRequest
			select {
			case request = <-received:
			case <-time.After(15 * time.Second):
				t.Fatal("真实引擎没有发出权限审批")
			}
			require.Equal(t, interactiveprotocol.PermissionApproval, request.Method)
			nativeParams = request.Params
			// 先确认非法答案已真实发出，再以同连接 RPC 为屏障，之后才允许 Discord 回答。
			require.Eventually(t, func() bool {
				trace.mu.Lock()
				defer trace.mu.Unlock()
				for _, message := range trace.messages {
					id, _ := json.Marshal(message["id"])
					if message["direction"] == "client" && string(id) == string(request.ID) && message["result"] != nil {
						return true
					}
				}
				return false
			}, 2*time.Second, 10*time.Millisecond, "必须先发出非法 Desktop 回答")
			require.NoError(t, client.Call(ctx, "thread/read", map[string]any{"threadId": thread}, nil))
			discord.deliverUntil(t, ctx, func() bool {
				return f.db.QueryRowContext(ctx, "SELECT q.id FROM codex_interactive_requests q JOIN codex_thread_controls c ON c.id=q.control_id WHERE c.engine=$3 AND q.thread_id=$1 AND q.app_server_request_id=$2::jsonb AND q.status='pending' AND q.discord_message_id IS NOT NULL", thread, request.ID, engine).Scan(&controlID) == nil
			})
			if expected == "" || engine == runtimeidentity.Claude {
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
			answer, answerErr := manager.AnswerInteractive(ctx, f.guildID, controlID, 0, option, "")
			require.NoError(t, answerErr)
			require.True(t, answer.Complete)
			// 已决议后的冲突答案不能改变持久化赢家。
			_, answerErr = manager.AnswerInteractive(ctx, f.guildID, controlID, 0, 1, "")
			require.NoError(t, answerErr)
			require.NoError(t, f.db.QueryRowContext(ctx, "SELECT answer FROM codex_interactive_requests WHERE id=$1 AND status='resolved'", controlID).Scan(&nativeAnswer))
		}
		awaitBootstrapTurn(t, ctx, events)
		awaitControlRunCount(t, ctx, f, engine, 2+index)
		if active.allowed {
			expected += active.id + "\n"
		}
		if engine == runtimeidentity.Claude && !active.allowed {
			require.NoFileExists(t, scenario.path, "拒绝权限后真实 Write 不能产生副作用")
		} else {
			content, readErr := os.ReadFile(scenario.path)
			require.NoError(t, readErr)
			wanted := expected
			if engine == runtimeidentity.Claude {
				wanted = active.id + "\n"
			}
			require.Equal(t, wanted, string(content), "权限失效/拒绝后不能写入；授权工具内容必须准确")
		}
		scenario.mu.Lock()
		observed := active
		scenario.mu.Unlock()
		require.True(t, observed.commandSeen, "原生工具结果必须回到模型")
		if !active.executeOnly {
			require.True(t, observed.permissionSeen, "真实权限审批结果必须回到模型")
		}
		nativeReturned := observed.commandSeen
		if engine == runtimeidentity.Claude {
			require.Equal(t, 1, observed.writeCalls, "每个回合只提案一次真实 Write")
			wantedPermissions := 1
			if active.executeOnly {
				wantedPermissions = 0
			}
			require.Equal(t, wantedPermissions, observed.permissionCalls)
		}
		evidence = append(evidence, map[string]any{"case": active.id, "scope": active.scope,
			"grantRoot": scenario.grantRoot, "effectPath": scenario.path,
			"allowed": active.allowed, "nativeResultReturned": nativeReturned, "controlRequestId": controlID,
			"method": interactiveprotocol.PermissionApproval, "nativeParams": nativeParams, "nativeAnswer": nativeAnswer})
	}
	// 已有 session 授权只覆盖旧目录；新目录必须产生真实的 pending 请求。
	restartRoot := filepath.Join(home, "permission-restart-scope")
	require.NoError(t, os.Mkdir(restartRoot, 0o700))
	restartPath := filepath.Join(restartRoot, "permission-restart-forbidden.txt")
	scenario.mu.Lock()
	scenario.grantRoot = restartRoot
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
	restart := verifyControlInteractionRestart(t, ctx, f, app, engine, restartID, pending, json.RawMessage(`{"permissions":{},"scope":"turn"}`))
	require.NoFileExists(t, restartPath, "旧回答不得造成文件副作用")
	var otherInteractions int
	require.NoError(t, f.db.QueryRowContext(ctx, "SELECT count(*) FROM codex_interactive_requests q JOIN codex_thread_controls c ON c.id=q.control_id WHERE c.worker_id=$1 AND c.engine<>$2", f.workerID, engine).Scan(&otherInteractions))
	require.Zero(t, otherInteractions)
	saveBootstrapArtifact(t, "permission-effects", engine, map[string]any{"cases": evidence, "content": expected, "restart": restart})
}
