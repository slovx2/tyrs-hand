package httpapi

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

func TestWakeKindsMapping(t *testing.T) {
	require.Equal(t, []string{workerprotocol.WakeClaim}, wakeKinds("queued")[:1])
	require.ElementsMatch(t, []string{workerprotocol.WakeClaim,
		workerprotocol.WakeSessionTitle, workerprotocol.WakeThreadSync}, wakeKinds("reconcile"))
	require.Equal(t, []string{workerprotocol.WakeSSHConfig},
		wakeKinds(workerprotocol.WakeSSHConfig))
	require.Empty(t, wakeKinds("unknown-kind"))
}

func TestCollectWorkerWakeTargetsExplicitWorker(t *testing.T) {
	server := &Server{}
	workerID := uuid.New()
	payload, err := json.Marshal(workerWakeSignal{
		Kind: workerprotocol.WakeSSHConfig, WorkerID: workerID.String()})
	require.NoError(t, err)

	pending := make(map[uuid.UUID]map[string]bool)
	server.collectWorkerWake(context.Background(), pending, string(payload))
	require.True(t, pending[workerID][workerprotocol.WakeSSHConfig])
}

func TestCollectWorkerWakeIgnoresUnknownPayload(t *testing.T) {
	server := &Server{}
	pending := make(map[uuid.UUID]map[string]bool)
	server.collectWorkerWake(context.Background(), pending, "not-a-kind")
	require.Empty(t, pending)
}

func TestFlushWorkerWakesSkipsDisconnectedWorker(t *testing.T) {
	server := &Server{workerRPCConns: make(map[uuid.UUID]*workerRPCConnection)}
	workerID := uuid.New()
	pending := map[uuid.UUID]map[string]bool{
		workerID: {workerprotocol.WakeClaim: true},
	}
	server.flushWorkerWakes(context.Background(), pending)
	require.Empty(t, pending)
}
