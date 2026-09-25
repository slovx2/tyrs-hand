package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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

func TestRecoveryQueueDoesNotBlockHeartbeatOrActiveClaims(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var heartbeats, claims, executing, peak atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.URL.Path == "/worker/v1/identity":
			_ = json.NewEncoder(w).Encode(workerprotocol.WorkerIdentityResponse{WorkerID: uuid.New(), ProtocolVersion: workerprotocol.Version})
		case req.URL.Path == "/worker/v1/claims":
			claims.Add(1)
			_ = json.NewEncoder(w).Encode(workerprotocol.ClaimResponse{})
		case req.URL.Path == "/worker/v1/heartbeat":
			heartbeats.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(req.URL.Path, "/heartbeat"):
			_ = json.NewEncoder(w).Encode(workerprotocol.RunHeartbeatResponse{})
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	started := make(chan runtimeidentity.Engine, 2)
	release := make(chan struct{}, 2)
	process := runtimeProcessFunc(func(ctx context.Context, task *workerprotocol.Task, _ <-chan workerprotocol.RunCommand,
		_ func(string, json.RawMessage),
	) (workerprotocol.CompleteRequest, error) {
		count := executing.Add(1)
		defer executing.Add(-1)
		for old := peak.Load(); count > old && !peak.CompareAndSwap(old, count); old = peak.Load() {
		}
		started <- task.Snapshot.Runtime.Engine
		select {
		case <-ctx.Done():
			return workerprotocol.CompleteRequest{}, ctx.Err()
		case <-release:
			return workerprotocol.CompleteRequest{Result: codexcontrol.TurnResult{FinalAnswer: "recovered"}}, nil
		}
	})
	cfg := config.Config{WorkerDataRoot: t.TempDir(), WorkerMaxConcurrentJobs: 1,
		WorkerRole: "discord", ControlTimeout: time.Second, HeartbeatInterval: time.Hour,
		WorkerClaimFallbackInterval: 10 * time.Millisecond, NodeHeartbeatInterval: 10 * time.Millisecond}
	cfg.WorkerCredentialFile = filepath.Join(cfg.WorkerDataRoot, "credential")
	require.NoError(t, writeCredential(cfg.WorkerCredentialFile, "test-credential"))
	runner, err := NewRunner(cfg, workerprotocol.NewClient(server.URL, "test-credential", time.Second), process, zap.NewNop())
	require.NoError(t, err)
	newTestExecutor(t, runner, runtimeidentity.Claude, process)
	for _, executor := range runner.executors {
		task := coordinatorTask(uuid.New(), uuid.New(), uuid.New(), "thread", 1)
		task.Claimed.SourceType = codexcontrol.SourceWorkspace
		task.Claimed.RunID = uuid.New()
		task.Claimed.ConfirmedTurnID = "confirmed"
		task.Snapshot.Runtime.Engine = executor.engine
		require.NoError(t, executor.journals.save(&runJournal{Task: task, NextSequence: 1}))
	}
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	defer func() { cancel(); <-done }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("没有开始恢复")
	}
	// 第二个 Journal 正等待唯一并发槽；首个长任务不能挡住节点心跳或 interrupt/steer 的领取。
	require.Eventually(t, func() bool { return heartbeats.Load() >= 2 && claims.Load() >= 2 }, time.Second, 10*time.Millisecond)
	require.Empty(t, started, "恢复过程也必须遵守全 Worker 并发上限")
	release <- struct{}{}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("释放并发槽后另一引擎没有恢复")
	}
	require.EqualValues(t, 1, peak.Load())
}
