package hostworker

import (
	"context"
	"errors"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/appserverhub"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type rejectedRecoveryBinding struct {
	appserverhub.PassThroughController
}

func (rejectedRecoveryBinding) RebindRuntime(context.Context, *appserverhub.Client, int64) error {
	return errors.New("fixture rebind failed")
}

func TestRecoveryLogsStartAndRebindFailures(t *testing.T) {
	for _, phase := range []string{"start", "rebind"} {
		t.Run(phase, func(t *testing.T) {
			core, logs := observer.New(zap.ErrorLevel)
			failed := &appServerGeneration{done: make(chan struct{})}
			close(failed.done)
			runtime := &Runtime{current: failed, options: RuntimeOptions{
				Engine: runtimeidentity.Pi, Logger: zap.New(core), Controller: rejectedRecoveryBinding{},
			}}
			runtime.start = func(context.Context) (*appServerGeneration, error) {
				if phase == "start" {
					return nil, errors.New("fixture start failed")
				}
				return &appServerGeneration{done: make(chan struct{})}, nil
			}
			require.ErrorContains(t, runtime.recoverAfterDesktopFailure(t.Context(), failed), "fixture "+phase+" failed")
			require.Equal(t, failed, runtime.current)
			require.Equal(t, 1, logs.Len())
			require.Equal(t, "pi", logs.All()[0].ContextMap()["engine"])
			require.Equal(t, "fixture "+phase+" failed", logs.All()[0].ContextMap()["error"])
		})
	}
}
