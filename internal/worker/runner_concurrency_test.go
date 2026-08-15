package worker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

func TestRunnerUsesSingleAllClaim(t *testing.T) {
	runner := &Runner{cfg: config.Config{WorkerRole: "all"}}
	require.Equal(t, "all", runner.claimRole())
	require.Equal(t, []string{"github", "discord"}, runner.roles())
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
	runner := &Runner{cfg: config.Config{WorkerID: "worker-test", WorkerRole: "discord",
		WorkerProtocolVersion: workerprotocol.Version, WorkerMaxConcurrentJobs: 2,
		WorkerSSHListenAddr: "127.0.0.1:2222"},
		client: workerprotocol.NewClient(server.URL, "credential", time.Second)}
	runner.SetSSHHostKeyFingerprint("SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")

	require.NoError(t, runner.sendHeartbeat(t.Context()))
	require.Equal(t, "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		(<-requests).SSHHostKeyFingerprint)
}
