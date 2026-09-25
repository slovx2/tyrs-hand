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

func TestRunnerReceivesActiveCommandsWhileOtherEngineWaitsForCapacity(t *testing.T) {
	primary := coordinatorTask(uuid.New(), uuid.New(), uuid.New(), "active", 5)
	primary.Claimed.SourceType = codexcontrol.SourceWorkspace
	primary.Snapshot.Runtime.Engine = runtimeidentity.Codex
	pending := coordinatorTask(uuid.New(), uuid.New(), uuid.New(), "pending", 5)
	pending.Claimed.SourceType = codexcontrol.SourceWorkspace
	pending.Snapshot.Runtime.Engine = runtimeidentity.Claude
	steer := primary
	steer.Claimed.ID = uuid.New()
	steer.Claimed.Sequence++
	steer.Claimed.Instruction = "continue-active"
	var decided, commanded, waiting atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.URL.Path == "/worker/v1/identity":
			_ = json.NewEncoder(w).Encode(workerprotocol.WorkerIdentityResponse{WorkerID: uuid.New(), ProtocolVersion: workerprotocol.Version})
		case req.URL.Path == "/worker/v1/claims":
			var request struct {
				OnlyActive bool `json:"onlyActive"`
			}
			_ = json.NewDecoder(req.Body).Decode(&request)
			response := workerprotocol.ClaimResponse{}
			if req.Header.Get(workerprotocol.EngineHeader) == string(runtimeidentity.Claude) {
				waiting.Store(true)
				if !request.OnlyActive {
					response.Task = &pending
				}
			} else if !decided.Load() {
				response.Task = &primary
			} else if waiting.Load() && !commanded.Load() {
				response.Task = &steer
			}
			_ = json.NewEncoder(w).Encode(response)
		case req.URL.Path == "/worker/v1/inputs/decide":
			decided.Store(true)
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(req.URL.Path, "/heartbeat"):
			_ = json.NewEncoder(w).Encode(workerprotocol.RunHeartbeatResponse{})
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(server.Close)
	process := runtimeProcessFunc(func(ctx context.Context, task *workerprotocol.Task, commands <-chan workerprotocol.RunCommand,
		_ func(string, json.RawMessage)) (workerprotocol.CompleteRequest, error) {
		if task.Snapshot.Runtime.Engine == runtimeidentity.Claude {
			<-ctx.Done()
			return workerprotocol.CompleteRequest{}, ctx.Err()
		}
		select {
		case command := <-commands:
			if command.ID == steer.Claimed.ID && command.Instruction == "continue-active" {
				commanded.Store(true)
			}
		case <-ctx.Done():
		}
		return workerprotocol.CompleteRequest{}, nil
	})
	cfg := config.Config{WorkerDataRoot: t.TempDir(), WorkerRole: "discord", WorkerMaxConcurrentJobs: 1,
		ControlTimeout: time.Second, HeartbeatInterval: time.Second, WorkerClaimFallbackInterval: 10 * time.Millisecond}
	cfg.WorkerCredentialFile = filepath.Join(cfg.WorkerDataRoot, "credential")
	require.NoError(t, writeCredential(cfg.WorkerCredentialFile, "test"))
	runner, err := NewRunner(cfg, workerprotocol.NewClient(server.URL, "test", time.Second), process, zap.NewNop())
	require.NoError(t, err)
	newTestExecutor(t, runner, runtimeidentity.Claude, process)
	ctx, cancel := context.WithTimeout(t.Context(), 600*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, runner.Run(ctx), context.DeadlineExceeded)
	require.True(t, waiting.Load())
	require.True(t, commanded.Load(), "另一个引擎的新任务排队不能阻止活动会话接收命令")
}
