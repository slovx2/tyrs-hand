//go:build integration

package bootstrap

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/internal/worker"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/slovx2/tyrs-hand/internal/workerregistry"
	"github.com/stretchr/testify/require"
)

func TestWorkerControlNativeQueueBindingRealSSH(t *testing.T) {
	requireControlNetworkIsolation(t)
	for _, restart := range []bool{false, true} {
		verifyNativeQueueBinding(t, restart)
	}
}

// 管理绑定变更由数据库夹具注入；真实 Control、Worker 和官方 CLI 决定全部执行结果。
func verifyNativeQueueBinding(t *testing.T, restart bool) {
	t.Helper()
	t.Logf("队列跨 Workspace 验收，完整 Worker 重启=%t", restart)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	var calls, denied, accepted atomic.Int64
	model := httptest.NewServer(nativeQueueBindingModel(t, &calls, &denied, &accepted))
	t.Cleanup(model.Close)
	ready := make(chan struct{})
	close(ready)
	f := newControlRuntimeFixture(t, ctx, model.URL, ready)
	discord := startControlDiscordFixture(t, ctx, f)
	start := func() (*WorkerApp, func()) {
		workerCtx, cancelWorker := context.WithCancel(ctx)
		app, cleanup, err := InitializeWorker(workerCtx, f.cfg)
		require.NoError(t, err)
		done := make(chan error, 1)
		go func() { done <- app.Run(workerCtx) }()
		var once sync.Once
		stop := func() { once.Do(func() { cancelWorker(); <-done; cleanup() }) }
		t.Cleanup(stop)
		return app, stop
	}
	app, stop := start()
	defer func() { stop() }()
	entry, err := app.Runtimes.Entry(runtimeidentity.Codex)
	require.NoError(t, err)
	client, _ := connectBootstrapSSH(t, ctx, entry, f.signer)
	newThread := func() string {
		var response struct{ Thread struct{ ID string } }
		require.NoError(t, client.Call(ctx, "thread/start", map[string]any{
			"cwd": f.cfg.WorkerWorkspaceRoot, "approvalPolicy": "never",
			"sandbox": "danger-full-access", "historyMode": "paginated",
		}, &response))
		return response.Thread.ID
	}
	threadID := newThread()
	events := client.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	var warmup struct{ Turn struct{ ID string } }
	require.NoError(t, client.Call(ctx, "turn/start", map[string]any{
		"threadId": threadID, "input": []map[string]string{{"type": "text", "text": "QUEUE_BINDING_WARMUP"}},
	}, &warmup))
	watcher := channelsTurnWatcher{events: events}
	watcher.awaitCompleted(t, ctx, warmup.Turn.ID, nil)
	events.Close()
	awaitControlRunCount(t, ctx, f, runtimeidentity.Codex, 1)
	discord.deliverUntil(t, ctx, func() bool {
		var pending int
		return f.db.QueryRowContext(ctx, "SELECT count(*) FROM integration_outbox WHERE status<>'completed'").Scan(&pending) == nil && pending == 0
	})
	// 卸载原生线程，保存入队身份后再变更绑定，不依赖模型速度制造竞态。
	require.NoError(t, entry.Runtime.Restart())
	client, _ = connectBootstrapSSH(t, ctx, entry, f.signer)
	clientID := uuid.NewString()
	var added struct{ QueuedSubmission struct{ ID string } }
	require.NoError(t, client.Call(ctx, "thread/queue/add", map[string]any{
		"threadId": threadID, "clientUserMessageId": clientID,
		"input": []map[string]string{{"type": "text", "text": "QUEUE_BINDING_OLD"}},
	}, &added))
	require.NotEmpty(t, added.QueuedSubmission.ID)
	require.EqualValues(t, 1, calls.Load(), "尚未加载的原生队列不能请求模型")
	admission := readQueueBindingAdmission(t, f.cfg.WorkerDataRoot, clientID)
	require.Equal(t, f.workspaceID, admission.Manifest.WorkspaceID)
	require.Equal(t, f.workspaceID, admission.Task.Snapshot.Session.Project.WorkspaceID)
	if restart {
		stop()
	}
	nextWorkspace := swapQueueFixtureWorkspace(t, ctx, f)
	if restart {
		app, stop = start()
		entry, err = app.Runtimes.Entry(runtimeidentity.Codex)
		require.NoError(t, err)
		client, _ = connectBootstrapSSH(t, ctx, entry, f.signer)
	}
	require.Eventually(t, func() bool {
		manifest, err := worker.LoadCachedWorkspaceManifest(f.cfg.WorkerDataRoot)
		return err == nil && manifest.WorkspaceID == nextWorkspace
	}, 15*time.Second, 25*time.Millisecond, "Worker 必须实际接收新 Control 绑定")
	events = client.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	defer events.Close()
	require.NoError(t, client.Call(ctx, "thread/resume", map[string]any{"threadId": threadID}, nil))
	watcher = channelsTurnWatcher{events: events}
	oldTurn := watcher.awaitCompleted(t, ctx, "", nil)
	require.EqualValues(t, 1, denied.Load(), "旧队列的真实 Control 工具调用必须被拒绝")
	var retained struct {
		Task           workerprotocol.Task
		DesktopRequest *workerprotocol.DesktopTurnPrepareRequest
		Result         json.RawMessage
	}
	require.Eventually(t, func() bool {
		path := filepath.Join(f.cfg.WorkerDataRoot, "control-state", "runs", admission.Task.Claimed.RunID.String()+".json")
		data, err := os.ReadFile(path)
		return err == nil && json.Unmarshal(data, &retained) == nil && retained.DesktopRequest != nil &&
			retained.Task.Claimed.ConfirmedTurnID == oldTurn && len(retained.Result) > 0 && string(retained.Result) != "null"
	}, 10*time.Second, 25*time.Millisecond, "旧队列的真实终态必须保存在原 Run Journal")
	require.Equal(t, admission.Task.Claimed.RunID, retained.Task.Claimed.RunID)
	require.Equal(t, f.workspaceID, retained.Task.Snapshot.Session.Project.WorkspaceID)
	require.Equal(t, f.workspaceID, retained.DesktopRequest.WorkspaceID)
	var foreign, schedules int
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT count(*) FROM codex_thread_controls
		WHERE external_thread_id=$1 AND workspace_id=$2`, threadID, nextWorkspace).Scan(&foreign))
	require.Zero(t, foreign, "新 Workspace 不能认领旧队列会话或 metadata")
	require.NoError(t, f.db.QueryRowContext(ctx, "SELECT count(*) FROM scheduled_tasks WHERE name='QUEUE_BINDING_OLD'").Scan(&schedules))
	require.Zero(t, schedules, "旧队列不能在任一 Workspace 创建定时任务")
	// 新绑定的正常回合仍能调用平台工具，避免以禁用全部 Control 功能掩盖串权。
	newThreadID := newThread()
	newEvents := client.Subscribe(codex.ThreadFilter{ThreadID: newThreadID})
	defer newEvents.Close()
	require.NoError(t, client.Call(ctx, "turn/start", map[string]any{
		"threadId": newThreadID, "input": []map[string]string{{"type": "text", "text": "QUEUE_BINDING_NEW"}},
	}, nil))
	newWatcher := channelsTurnWatcher{events: newEvents}
	newTurn := newWatcher.awaitCompleted(t, ctx, "", nil)
	require.EqualValues(t, 1, accepted.Load())
	require.Eventually(t, func() bool {
		var count int
		err := f.db.QueryRowContext(ctx, `SELECT count(*) FROM codex_turn_runs run
			JOIN codex_thread_controls control ON control.id=run.control_id
			JOIN tool_calls tool ON tool.run_id=run.id
			WHERE run.confirmed_codex_turn_id=$1 AND run.status='completed'
			AND control.workspace_id=$2 AND tool.call_id='QUEUE_BINDING_NEW'`, newTurn, nextWorkspace).Scan(&count)
		return err == nil && count == 1
	}, 15*time.Second, 25*time.Millisecond, "新回合和真实平台工具必须落到新 Workspace 的唯一 Control Run")
	require.NoError(t, f.db.QueryRowContext(ctx, "SELECT count(*) FROM scheduled_tasks WHERE name='QUEUE_BINDING_NEW' AND workspace_id=$1", nextWorkspace).Scan(&schedules))
	require.Equal(t, 1, schedules, "真实工具副作用只归属于新 Workspace")
	require.EqualValues(t, 5, calls.Load(), "预热一次，新旧工具各请求及回模一次")
	history, err := codex.NewRuntime(client).ReadThread(ctx, threadID)
	require.NoError(t, err)
	require.Len(t, history.Turns, 2, "旧队列只执行一次且历史仍可读取")
	stop()
	app, stop = start()
	entry, err = app.Runtimes.Entry(runtimeidentity.Codex)
	require.NoError(t, err)
	client, _ = connectBootstrapSSH(t, ctx, entry, f.signer)
	var queue struct{ Data []json.RawMessage }
	require.NoError(t, client.Call(ctx, "thread/queue/list", map[string]any{"threadId": threadID}, &queue))
	require.Empty(t, queue.Data, "完成后的旧队列不能在新绑定重启时复活")
	history, err = codex.NewRuntime(client).ReadThread(ctx, threadID)
	require.NoError(t, err)
	require.Len(t, history.Turns, 2)
	require.EqualValues(t, 5, calls.Load(), "新绑定重启后只读恢复，不能重放模型与副作用")
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT count(*) FROM codex_thread_controls
		WHERE external_thread_id=$1 AND workspace_id=$2`, threadID, nextWorkspace).Scan(&foreign))
	require.Zero(t, foreign)
	saveBootstrapArtifact(t, "queue-binding", runtimeidentity.Codex, map[string]any{
		"workerRestarted": restart, "originalWorkspace": f.workspaceID, "newWorkspace": nextWorkspace,
		"originalRunId": admission.Task.Claimed.RunID, "oldThreadId": threadID, "oldTurnId": oldTurn,
		"newThreadId": newThreadID, "newTurnId": newTurn, "rejectedTools": denied.Load(), "newSchedules": schedules,
	})
}

type queueBindingAdmission struct {
	Manifest workerprotocol.WorkspaceManifest `json:"manifest"`
	Task     workerprotocol.Task              `json:"task"`
}

func swapQueueFixtureWorkspace(t *testing.T, ctx context.Context, f controlRuntimeFixture) uuid.UUID {
	t.Helper()
	retired, _, err := workerregistry.NewService(f.db).Create(ctx, "queue-original-owner", []string{"discord"}, 1)
	require.NoError(t, err)
	next := uuid.New()
	tx, err := f.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, "UPDATE worker_workspaces SET worker_id=$1 WHERE id=$2", retired.ID, f.workspaceID)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `INSERT INTO discord_members(guild_id,discord_user_id,username)
		VALUES ($1,'1002','queue-new-owner')`, f.guildID)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `INSERT INTO worker_workspaces(id,worker_id,guild_id,owner_discord_user_id)
		VALUES ($1,$2,$3,'1002')`, next, f.workerID, f.guildID)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `INSERT INTO workspace_projects(workspace_id,relative_path,name,project_kind,
		availability_status,project_source,host_path)
		VALUES ($1,'workspaces','Workspace','directory','available','workspace_root',$2)`, next, f.cfg.WorkerWorkspaceRoot)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	return next
}

func nativeQueueBindingModel(t *testing.T, calls, denied, accepted *atomic.Int64) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		require.NoError(t, err)
		var request struct {
			Input []struct {
				Type, Role      string
				CallID          string `json:"call_id"`
				Content, Output json.RawMessage
			}
			Text struct{ Format struct{ Type string } }
		}
		require.NoError(t, json.Unmarshal(body, &request))
		if request.Text.Format.Type == "json_schema" {
			bootstrapEvent(w, "response.created", map[string]any{"response": map[string]string{"id": "binding-title"}})
			bootstrapEvent(w, "response.output_item.done", map[string]any{"item": map[string]any{"type": "message", "role": "assistant", "id": "binding-title-message", "content": []map[string]string{{"type": "output_text", "text": `{"title":"跨 Workspace 队列"}`}}}})
			bootstrapEvent(w, "response.completed", map[string]any{"response": map[string]string{"id": "binding-title"}})
			return
		}
		require.LessOrEqual(t, calls.Add(1), int64(5), "不能重放模型或工具")
		marker := ""
		for _, input := range request.Input {
			if input.Role == "user" {
				for _, candidate := range []string{"QUEUE_BINDING_WARMUP", "QUEUE_BINDING_OLD", "QUEUE_BINDING_NEW"} {
					if strings.Contains(string(input.Content), candidate) {
						marker = candidate
					}
				}
			}
		}
		require.NotEmpty(t, marker)
		if marker == "QUEUE_BINDING_WARMUP" {
			bootstrapModelText(w, false)
			return
		}
		for _, input := range request.Input {
			if input.Type == "function_call_output" && input.CallID == marker {
				if marker == "QUEUE_BINDING_OLD" {
					require.Contains(t, string(input.Output), "Workspace 绑定已失效")
					denied.Add(1)
				} else {
					require.Contains(t, string(input.Output), "QUEUE_BINDING_NEW")
					accepted.Add(1)
				}
				bootstrapModelText(w, false)
				return
			}
		}
		require.Contains(t, string(body), "automation_update", "仅调用原生运行时已声明的工具")
		args, err := json.Marshal(map[string]any{"action": "create", "kind": "heartbeat",
			"name": marker, "prompt": marker + "_FOLLOWUP", "schedule": "DTSTART:20300102T000000Z\nRRULE:FREQ=DAILY"})
		require.NoError(t, err)
		bootstrapEvent(w, "response.created", map[string]any{"response": map[string]string{"id": marker}})
		bootstrapEvent(w, "response.output_item.done", map[string]any{"item": map[string]any{
			"type": "function_call", "namespace": "tyrs_hand", "name": "automation_update", "call_id": marker, "arguments": string(args),
		}})
		bootstrapEvent(w, "response.completed", map[string]any{"response": map[string]string{"id": marker}})
	})
}

func readQueueBindingAdmission(t *testing.T, root, clientID string) queueBindingAdmission {
	t.Helper()
	var found queueBindingAdmission
	require.NoError(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Base(filepath.Dir(path)) != "queues" || !strings.HasSuffix(path, ".json") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var record struct {
			ClientID string `json:"clientUserMessageId"`
		}
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		if record.ClientID == clientID {
			return json.Unmarshal(data, &found)
		}
		return nil
	}))
	require.NotEqual(t, uuid.Nil, found.Task.Claimed.RunID)
	require.NotNil(t, found.Task.Snapshot.Session)
	require.NotNil(t, found.Task.Snapshot.Session.Project)
	return found
}
