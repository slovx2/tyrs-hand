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

func TestRunnerDoesNotReplayUnconfirmedControlInput(t *testing.T) {
	for _, engine := range []runtimeidentity.Engine{runtimeidentity.Codex, runtimeidentity.Claude} {
		t.Run(string(engine), func(t *testing.T) {
			task := coordinatorTask(uuid.New(), uuid.New(), uuid.New(), "same-thread", 5)
			task.Snapshot.Runtime.Engine = engine
			task.Claimed.SourceType = codexcontrol.SourceWorkspace
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/worker/v1/identity":
					_ = json.NewEncoder(w).Encode(workerprotocol.WorkerIdentityResponse{WorkerID: uuid.New(), ProtocolVersion: workerprotocol.Version})
				case r.URL.Path == "/worker/v1/claims":
					response := workerprotocol.ClaimResponse{}
					if r.Header.Get(workerprotocol.EngineHeader) == string(engine) {
						response.Task = &task
					}
					_ = json.NewEncoder(w).Encode(response)
				case r.URL.Path == "/worker/v1/inputs/decide":
					http.Error(w, "登记结果未知", http.StatusBadGateway)
				case strings.HasSuffix(r.URL.Path, "/complete"):
					http.Error(w, "Run 尚未登记", http.StatusNotFound)
				default:
					w.WriteHeader(http.StatusNoContent)
				}
			}))
			t.Cleanup(server.Close)
			var effects atomic.Int32
			process := runtimeProcessFunc(func(context.Context, *workerprotocol.Task, <-chan workerprotocol.RunCommand,
				func(string, json.RawMessage),
			) (workerprotocol.CompleteRequest, error) {
				effects.Add(1)
				return workerprotocol.CompleteRequest{Result: codexcontrol.TurnResult{FinalAnswer: "已执行副作用"}}, nil
			})
			cfg := config.Config{WorkerDataRoot: t.TempDir(), WorkerRole: "discord", WorkerMaxConcurrentJobs: 1,
				ControlTimeout: time.Second, HeartbeatInterval: time.Second, WorkerClaimFallbackInterval: time.Millisecond}
			cfg.WorkerCredentialFile = filepath.Join(cfg.WorkerDataRoot, "credential")
			require.NoError(t, writeCredential(cfg.WorkerCredentialFile, "test"))
			runner, err := NewRunner(cfg, workerprotocol.NewClient(server.URL, "test", time.Second), process, zap.NewNop())
			require.NoError(t, err)
			if engine == runtimeidentity.Claude {
				newTestExecutor(t, runner, engine, process)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
			defer cancel()
			require.ErrorIs(t, runner.Run(ctx), context.DeadlineExceeded)
			require.EqualValues(t, 1, effects.Load(), "Control 未确认输入仍会反复返回，不能重复执行")
			stored, err := runner.executors[len(runner.executors)-1].journals.loadAll()
			require.NoError(t, err)
			require.Len(t, stored, 1, "未确认的执行结果必须留待对账")
			require.Equal(t, "已执行副作用", stored[0].Result.FinalAnswer)
			require.True(t, stored[0].ControlAbandoned)
			// 重建执行器，仅读取磁盘状态；旧进程内存中的去重集合不能参与此断言。
			restarted, err := NewRunner(cfg, workerprotocol.NewClient(server.URL, "test", time.Second), process, zap.NewNop())
			require.NoError(t, err)
			if engine == runtimeidentity.Claude {
				previous := runner.executors[1]
				journalStore, err := newJournalStore(previous.cfg.WorkerDataRoot)
				require.NoError(t, err)
				require.NoError(t, restarted.addExecutor(&runtimeExecutor{engine: engine, cfg: previous.cfg,
					client: previous.client, processor: process, journals: journalStore,
					coordinator: newRunCoordinator(journalStore), logger: zap.NewNop(),
					wake: newWakeSignals(), claimWake: restarted.wake}))
			}
			restartCtx, stop := context.WithTimeout(t.Context(), 200*time.Millisecond)
			defer stop()
			require.ErrorIs(t, restarted.Run(restartCtx), context.DeadlineExceeded)
			require.EqualValues(t, 1, effects.Load(), "重启不能重新执行待对账的副作用")
		})
	}
}
