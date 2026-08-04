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

func TestSuperviseWorkerStopsWhenAppServerExits(t *testing.T) {
	runtimeDone := make(chan struct{})
	runtimeCause := errors.New("app-server stopped")
	runnerStopped := make(chan struct{})
	run := func(ctx context.Context) error {
		<-ctx.Done()
		close(runnerStopped)
		return ctx.Err()
	}
	close(runtimeDone)
	err := superviseWorker(context.Background(), run, func() <-chan struct{} {
		return runtimeDone
	}, func() error { return runtimeCause })
	require.ErrorIs(t, err, runtimeCause)
	select {
	case <-runnerStopped:
	default:
		t.Fatal("App Server 退出后没有停止 Runner")
	}
}

func TestSuperviseWorkerReturnsRunnerFailure(t *testing.T) {
	runnerCause := errors.New("runner stopped")
	runtimeDone := make(chan struct{})
	err := superviseWorker(context.Background(), func(context.Context) error {
		return runnerCause
	}, func() <-chan struct{} { return runtimeDone }, func() error { return nil })
	require.ErrorIs(t, err, runnerCause)
}
