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
			// healthy 为 false 时模拟 Control 网关持续 502：登记结果未知，必须保留 Journal 重试。
			var healthy atomic.Bool
			var decided, completed atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/worker/v1/identity":
					_ = json.NewEncoder(w).Encode(workerprotocol.WorkerIdentityResponse{WorkerID: uuid.New(), ProtocolVersion: workerprotocol.Version})
				case r.URL.Path == "/worker/v1/claims":
					response := workerprotocol.ClaimResponse{}
					if r.Header.Get(workerprotocol.EngineHeader) == string(engine) && completed.Load() == 0 {
						response.Task = &task
					}
					_ = json.NewEncoder(w).Encode(response)
				case !healthy.Load():
					http.Error(w, "登记结果未知", http.StatusBadGateway)
				case r.URL.Path == "/worker/v1/inputs/decide":
					decided.Add(1)
					w.WriteHeader(http.StatusNoContent)
				case strings.HasSuffix(r.URL.Path, "/complete"):
					require.Positive(t, decided.Load(), "终态前必须先补登记 Run")
					completed.Add(1)
					w.WriteHeader(http.StatusNoContent)
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
			claudeDataRoot := t.TempDir()
			// 每次都重建执行器，仅读取磁盘状态；旧进程内存中的去重集合不能参与断言。
			newRunner := func() *Runner {
				runner, err := NewRunner(cfg, workerprotocol.NewClient(server.URL, "test", time.Second), process, zap.NewNop())
				require.NoError(t, err)
				if engine == runtimeidentity.Claude {
					// 与生产一致，Claude 执行器每次重启都复用同一数据目录。
					executorCfg := runner.cfg
					executorCfg.WorkerDataRoot = claudeDataRoot
					store, err := newJournalStore(claudeDataRoot)
					require.NoError(t, err)
					client, err := runner.client.ForEngine(engine)
					require.NoError(t, err)
					require.NoError(t, runner.addExecutor(&runtimeExecutor{engine: engine, cfg: executorCfg,
						client: client, processor: process, journals: store,
						coordinator: newRunCoordinator(store), logger: zap.NewNop(),
						wake: newWakeSignals(), claimWake: runner.wake}))
				}
				return runner
			}
			loadJournals := func(runner *Runner) []*runJournal {
				stored, err := runner.executors[len(runner.executors)-1].journals.loadAll()
				require.NoError(t, err)
				return stored
			}
			runFor := func(runner *Runner, timeout time.Duration) {
				ctx, cancel := context.WithTimeout(t.Context(), timeout)
				defer cancel()
				require.ErrorIs(t, runner.Run(ctx), context.DeadlineExceeded)
			}

			first := newRunner()
			runFor(first, 500*time.Millisecond)
			require.EqualValues(t, 1, effects.Load(), "Control 未确认输入仍会反复返回，不能重复执行")
			require.Zero(t, completed.Load(), "502 期间不能提交终态")
			stored := loadJournals(first)
			require.Len(t, stored, 1, "未确认的执行结果必须留待对账")
			require.Equal(t, "已执行副作用", stored[0].Result.FinalAnswer)
			require.False(t, stored[0].ControlAbandoned, "可重试的 502 不能放弃补报")
			require.False(t, stored[0].TerminalDelivered)

			for restart := 1; restart <= 2; restart++ {
				runner := newRunner()
				runFor(runner, 200*time.Millisecond)
				require.EqualValues(t, 1, effects.Load(), "第 %d 次重启不能重新执行待对账的副作用", restart)
				require.Zero(t, completed.Load(), "Control 仍不可用时不能提交终态")
				stored = loadJournals(runner)
				require.Len(t, stored, 1, "第 %d 次重启后 Journal 必须保留", restart)
				require.False(t, stored[0].ControlAbandoned)
			}

			healthy.Store(true)
			recovered := newRunner()
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() { done <- recovered.Run(ctx) }()
			require.Eventually(t, func() bool {
				// 补报并发删除 Journal 时 loadAll 可能读到刚删除的文件，下一轮再判定。
				stored, err := recovered.executors[len(recovered.executors)-1].journals.loadAll()
				return err == nil && len(stored) == 0
			},
				5*time.Second, 10*time.Millisecond, "网络恢复后必须补报并清理 Journal")
			cancel()
			require.ErrorIs(t, <-done, context.Canceled)
			require.EqualValues(t, 1, effects.Load(), "恢复补报不能重新执行副作用")
			require.EqualValues(t, 1, completed.Load(), "恢复后恰好提交一次终态")
		})
	}
}
