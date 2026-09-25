package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestRunnerUsesSingleAllClaim(t *testing.T) {
	runner := &Runner{runtimeExecutor: &runtimeExecutor{cfg: config.Config{WorkerRole: "all"}}}
	require.Equal(t, "discord", runner.claimRole())
	require.Equal(t, []string{"discord"}, runner.roles())
}

func TestRunnerHeartbeatIncludesSSHHostKeyFingerprint(t *testing.T) {
	requests := make(chan workerprotocol.HeartbeatRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter,
		request *http.Request,
	) {
		require.Equal(t, "/worker/v1/heartbeat", request.URL.Path)
		var heartbeat workerprotocol.HeartbeatRequest
		require.NoError(t, json.NewDecoder(request.Body).Decode(&heartbeat))
		requests <- heartbeat
		response.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	runner := &Runner{runtimeExecutor: &runtimeExecutor{cfg: config.Config{WorkerID: "worker-test", WorkerRole: "discord",
		WorkerProtocolVersion: workerprotocol.Version, WorkerMaxConcurrentJobs: 2,
		WorkerSSHListenAddr: "127.0.0.1:2222"},
		client: workerprotocol.NewClient(server.URL, "credential", time.Second)}}
	runner.SetSSHHostKeyFingerprint("SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	runner.SetRuntimeReports(func() []workerprotocol.RuntimeReport {
		return []workerprotocol.RuntimeReport{
			{Engine: runtimeidentity.Codex, Status: "running", SSHListenAddress: ":2222"},
			{Engine: runtimeidentity.Claude, Status: "unavailable", SSHListenAddress: ":3333"},
		}
	})

	require.NoError(t, runner.sendHeartbeat(t.Context()))
	report := <-requests
	require.Equal(t, "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", report.SSHHostKeyFingerprint)
	require.Len(t, report.Runtimes, 2)
	require.Equal(t, "unavailable", report.Runtimes[1].Status)
}

func TestRunnerSkipsControlWhenSyncDisabled(t *testing.T) {
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits.Add(1)
	}))
	t.Cleanup(server.Close)
	runner, err := NewRunner(config.Config{
		WorkerDisableControlSync: true, WorkerDataRoot: t.TempDir(),
		WorkerMaxConcurrentJobs: 1, ControlTimeout: time.Second,
		HeartbeatInterval: 10 * time.Millisecond,
	}, workerprotocol.NewClient(server.URL, "credential", time.Second), noopProcessor{},
		zap.NewNop())
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	require.Zero(t, hits.Load())
}

type noopProcessor struct{}

func (noopProcessor) Process(context.Context, *workerprotocol.Task,
	<-chan workerprotocol.RunCommand, func(string, json.RawMessage),
) (workerprotocol.CompleteRequest, error) {
	return workerprotocol.CompleteRequest{}, nil
}
