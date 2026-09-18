package worker

import (
	"context"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

func TestWakeSignalsDeliversMatchingKind(t *testing.T) {
	wake := newWakeSignals()
	wake.Notify([]string{workerprotocol.WakeClaim})

	started := time.Now()
	require.True(t, wake.Wait(context.Background(), time.Hour, workerprotocol.WakeClaim))
	require.Less(t, time.Since(started), time.Second)

	// 已消费的唤醒不会再次触发。
	started = time.Now()
	require.True(t, wake.Wait(context.Background(), 20*time.Millisecond,
		workerprotocol.WakeClaim))
	require.GreaterOrEqual(t, time.Since(started), 15*time.Millisecond)
}

func TestWakeSignalsKeepsUnrelatedKinds(t *testing.T) {
	wake := newWakeSignals()
	wake.Notify([]string{workerprotocol.WakeSessionTitle})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.False(t, wake.Wait(ctx, time.Hour, workerprotocol.WakeClaim))

	require.True(t, wake.Wait(context.Background(), time.Hour,
		workerprotocol.WakeSessionTitle))
}

func TestWakeSignalsStopsOnContextCancel(t *testing.T) {
	wake := newWakeSignals()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	require.False(t, wake.Wait(ctx, time.Hour, workerprotocol.WakeClaim))
}

func TestWakeSignalsRejectsEmptyNotification(t *testing.T) {
	wake := newWakeSignals()
	wake.Notify(nil)
	started := time.Now()
	require.True(t, wake.Wait(context.Background(), 20*time.Millisecond))
	require.GreaterOrEqual(t, time.Since(started), 15*time.Millisecond)
}
