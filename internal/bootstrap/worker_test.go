package bootstrap

import (
	"context"
	"errors"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/worker"
	"github.com/stretchr/testify/require"
)

func TestInitializeWorkerAcquiresDataLockBeforeRuntime(t *testing.T) {
	root := t.TempDir()
	lock, err := worker.AcquireDataLock(root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, lock.Close()) })

	app, cleanup, err := InitializeWorker(context.Background(), config.Config{
		Environment: "development", WorkerDataRoot: root,
	})
	require.ErrorContains(t, err, "已经有 Worker 实例运行")
	require.Nil(t, app)
	require.Nil(t, cleanup)
}

func TestSuperviseWorkerStopsOnContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runnerStopped := make(chan struct{})
	run := func(ctx context.Context) error {
		<-ctx.Done()
		close(runnerStopped)
		return ctx.Err()
	}
	cancel()
	err := superviseWorker(ctx, run)
	require.ErrorIs(t, err, context.Canceled)
	select {
	case <-runnerStopped:
	default:
		t.Fatal("Worker Context 取消后没有停止 Runner")
	}
}

func TestSuperviseWorkerReturnsRunnerFailure(t *testing.T) {
	runnerCause := errors.New("runner stopped")
	err := superviseWorker(context.Background(), func(context.Context) error {
		return runnerCause
	})
	require.ErrorIs(t, err, runnerCause)
}
