package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type runtimeProcessFunc func(context.Context, *workerprotocol.Task, <-chan workerprotocol.RunCommand,
	func(string, json.RawMessage)) (workerprotocol.CompleteRequest, error)

func (f runtimeProcessFunc) Process(ctx context.Context, task *workerprotocol.Task,
	commands <-chan workerprotocol.RunCommand, report func(string, json.RawMessage),
) (workerprotocol.CompleteRequest, error) {
	return f(ctx, task, commands, report)
}

func newTestExecutor(t *testing.T, r *Runner, engine runtimeidentity.Engine, processor taskProcessor) *runtimeExecutor {
	t.Helper()
	cfg := r.cfg
	cfg.WorkerDataRoot = t.TempDir()
	store, err := newJournalStore(cfg.WorkerDataRoot)
	require.NoError(t, err)
	client, err := r.client.ForEngine(engine)
	require.NoError(t, err)
	executor := &runtimeExecutor{engine: engine, cfg: cfg, client: client, processor: processor,
		journals: store, coordinator: newRunCoordinator(store), logger: zap.NewNop(),
		wake: newWakeSignals(), claimWake: r.wake}
	require.NoError(t, r.addExecutor(executor))
	return executor
}

func TestRunnerDispatchesBothEnginesOnceWithSharedBudget(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var mu sync.Mutex
	decided := map[runtimeidentity.Engine]bool{}
	completed := map[runtimeidentity.Engine]int{}
	events := map[runtimeidentity.Engine]int{}
	// 刻意复用所有协议 ID；执行及补报只能按入口引擎路由。
	task := coordinatorTask(uuid.New(), uuid.New(), uuid.New(), "same-thread", 5)
	task.Claimed.SourceType = codexcontrol.SourceWorkspace
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		engine := runtimeidentity.Engine(req.Header.Get(workerprotocol.EngineHeader))
		mu.Lock()
		defer mu.Unlock()
		switch {
		case req.URL.Path == "/worker/v1/identity":
			_ = json.NewEncoder(w).Encode(workerprotocol.WorkerIdentityResponse{WorkerID: uuid.New(), ProtocolVersion: workerprotocol.Version})
		case req.URL.Path == "/worker/v1/claims":
			var result workerprotocol.ClaimResponse
			if !decided[engine] {
				copyTask := task
				copyTask.Snapshot.Runtime.Engine = engine
				result.Task = &copyTask
			}
			_ = json.NewEncoder(w).Encode(result)
		case req.URL.Path == "/worker/v1/inputs/decide":
			decided[engine] = true
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(req.URL.Path, "/complete"):
			completed[engine]++
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(req.URL.Path, "/events"):
			events[engine]++
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(req.URL.Path, "/heartbeat"):
			_ = json.NewEncoder(w).Encode(workerprotocol.RunHeartbeatResponse{})
		default:
			t.Errorf("意外请求 %s", req.URL.Path)
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(server.Close)
	started := make(chan runtimeidentity.Engine, 4)
	release := make(chan struct{}, 4)
	var active, maxActive atomic.Int32
	process := func(engine runtimeidentity.Engine) taskProcessor {
		return runtimeProcessFunc(func(ctx context.Context, received *workerprotocol.Task, _ <-chan workerprotocol.RunCommand,
			report func(string, json.RawMessage),
		) (workerprotocol.CompleteRequest, error) {
			if received.Snapshot.Runtime.Engine != engine {
				t.Errorf("引擎错投: %s -> %s", received.Snapshot.Runtime.Engine, engine)
			}
			count := active.Add(1)
			defer active.Add(-1)
			for old := maxActive.Load(); count > old && !maxActive.CompareAndSwap(old, count); old = maxActive.Load() {
			}
			started <- engine
			report("item.completed", json.RawMessage(`{"test":true}`))
			select {
			case <-ctx.Done():
				return workerprotocol.CompleteRequest{}, ctx.Err()
			case <-release:
				return workerprotocol.CompleteRequest{Result: codexcontrol.TurnResult{FinalAnswer: string(engine)}}, nil
			}
		})
	}
	cfg := config.Config{WorkerDataRoot: t.TempDir(), WorkerMaxConcurrentJobs: 1,
		WorkerRole: "discord", ControlTimeout: time.Second, HeartbeatInterval: time.Hour,
		WorkerClaimFallbackInterval: 10 * time.Millisecond, NodeHeartbeatInterval: time.Hour}
	cfg.WorkerCredentialFile = filepath.Join(cfg.WorkerDataRoot, "credential")
	require.NoError(t, writeCredential(cfg.WorkerCredentialFile, "test-credential"))
	runner, err := NewRunner(cfg, workerprotocol.NewClient(server.URL, "test-credential", time.Second), process(runtimeidentity.Codex), zap.NewNop())
	require.NoError(t, err)
	newTestExecutor(t, runner, runtimeidentity.Claude, process(runtimeidentity.Claude))
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	for _, engine := range []runtimeidentity.Engine{runtimeidentity.Codex, runtimeidentity.Claude} {
		select {
		case got := <-started:
			require.Equal(t, engine, got)
		case <-ctx.Done():
			t.Fatal("任务没有启动")
		}
		release <- struct{}{}
	}
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return completed[runtimeidentity.Codex] == 1 && completed[runtimeidentity.Claude] == 1
	}, 3*time.Second, 10*time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	require.EqualValues(t, 1, maxActive.Load())
	require.Empty(t, started, "相同输入不能执行两次")
	for _, executor := range runner.executors {
		stored, err := executor.journals.loadAll()
		require.NoError(t, err)
		require.Empty(t, stored, "终态确认后必须删除对应引擎的 Journal")
		mu.Lock()
		require.Equal(t, 1, events[executor.engine])
		mu.Unlock()
	}
}

func TestRunnerRejectsForeignJournalBeforeRecovery(t *testing.T) {
	runner, err := NewRunner(config.Config{WorkerDataRoot: t.TempDir(), WorkerMaxConcurrentJobs: 1, WorkerRole: "discord"},
		workerprotocol.NewClient("http://127.0.0.1:1", "test", time.Second), noopProcessor{}, zap.NewNop())
	require.NoError(t, err)
	claude := newTestExecutor(t, runner, runtimeidentity.Claude, noopProcessor{})
	task := coordinatorTask(uuid.New(), uuid.New(), uuid.New(), "same-thread", 5)
	task.Claimed.SourceType = codexcontrol.SourceWorkspace
	task.Claimed.RunID = uuid.New()
	require.NoError(t, claude.journals.save(&runJournal{Task: task}))
	var active sync.WaitGroup
	require.ErrorContains(t, runner.recoverJournals(t.Context(), &active), "目录引擎")
	require.Empty(t, runner.turnSlots)
}

func TestRunnerClaimFairnessAndForeignEngineRejection(t *testing.T) {
	var failCodex, wrongEngine atomic.Bool
	task := coordinatorTask(uuid.New(), uuid.New(), uuid.New(), "same-thread", 5)
	task.Claimed.SourceType = codexcontrol.SourceWorkspace
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		engine := runtimeidentity.Engine(req.Header.Get(workerprotocol.EngineHeader))
		if engine == runtimeidentity.Codex && failCodex.Load() {
			http.Error(w, "Codex unavailable", http.StatusServiceUnavailable)
			return
		}
		copyTask := task
		copyTask.Snapshot.Runtime.Engine = engine
		if engine == runtimeidentity.Codex && wrongEngine.Load() {
			copyTask.Snapshot.Runtime.Engine = runtimeidentity.Claude
		}
		_ = json.NewEncoder(w).Encode(workerprotocol.ClaimResponse{Task: &copyTask})
	}))
	t.Cleanup(server.Close)
	runner, err := NewRunner(config.Config{WorkerDataRoot: t.TempDir(), WorkerRole: "discord"},
		workerprotocol.NewClient(server.URL, "test", time.Second), noopProcessor{}, zap.NewNop())
	require.NoError(t, err)
	newTestExecutor(t, runner, runtimeidentity.Claude, noopProcessor{})
	for i := 0; i < 6; i++ {
		executor, received := runner.claimNext(t.Context())
		want := runtimeidentity.Codex
		if i%2 != 0 {
			want = runtimeidentity.Claude
		}
		require.Equal(t, want, executor.engine)
		require.Equal(t, want, received.Snapshot.Runtime.Engine)
	}
	failCodex.Store(true)
	executor, _ := runner.claimNext(t.Context())
	require.Equal(t, runtimeidentity.Claude, executor.engine, "单引擎领取失败不能阻止另一引擎")
	failCodex.Store(false)
	wrongEngine.Store(true)
	executor, _ = runner.claimNext(t.Context())
	require.Equal(t, runtimeidentity.Claude, executor.engine, "错误引擎响应必须丢弃，由正确客户端领取")
}

func TestRunnerRecoversEachRuntimeJournalWithoutReplayingConfirmedTools(t *testing.T) {
	var mu sync.Mutex
	delivered := map[runtimeidentity.Engine]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "/complete") {
			mu.Lock()
			delivered[runtimeidentity.Engine(req.Header.Get(workerprotocol.EngineHeader))]++
			mu.Unlock()
		}
		if strings.HasSuffix(req.URL.Path, "/heartbeat") {
			_ = json.NewEncoder(w).Encode(workerprotocol.RunHeartbeatResponse{})
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(server.Close)
	process := runtimeProcessFunc(func(context.Context, *workerprotocol.Task, <-chan workerprotocol.RunCommand,
		func(string, json.RawMessage),
	) (workerprotocol.CompleteRequest, error) {
		t.Error("已保存终态不得再次请求模型或执行工具")
		return workerprotocol.CompleteRequest{}, nil
	})
	runner, err := NewRunner(config.Config{WorkerDataRoot: t.TempDir(), WorkerRole: "discord", WorkerMaxConcurrentJobs: 1, ControlTimeout: time.Second},
		workerprotocol.NewClient(server.URL, "test", time.Second), process, zap.NewNop())
	require.NoError(t, err)
	newTestExecutor(t, runner, runtimeidentity.Claude, process)
	task := coordinatorTask(uuid.New(), uuid.New(), uuid.New(), "same-thread", 5)
	task.Claimed.RunID = uuid.New()
	task.Claimed.SourceType = codexcontrol.SourceWorkspace
	for _, executor := range runner.executors {
		copyTask := task
		copyTask.Snapshot.Runtime.Engine = executor.engine
		require.NoError(t, executor.journals.save(&runJournal{Task: copyTask,
			Result:        &codexcontrol.TurnResult{FinalAnswer: string(executor.engine)},
			PendingEvents: []workerprotocol.EventInput{{Sequence: 1, Type: "turn.completed", Payload: json.RawMessage(`{}`)}},
		}))
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var active sync.WaitGroup
	require.NoError(t, runner.recoverJournals(ctx, &active))
	active.Wait()
	require.NoError(t, ctx.Err())
	for _, executor := range runner.executors {
		stored, err := executor.journals.loadAll()
		require.NoError(t, err)
		require.Empty(t, stored)
		require.Equal(t, 1, delivered[executor.engine])
	}
}
