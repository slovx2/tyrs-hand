package worker

import (
	"testing"

	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestRuntimesAndRunnerShareOneBudget(t *testing.T) {
	cfg := config.Config{WorkerDataRoot: t.TempDir(), WorkerMaxConcurrentJobs: 2, WorkerDisableControlSync: true}
	codex := NewProcessor(t.Context(), cfg, nil, nil, nil, zap.NewNop())
	cfg.WorkerDataRoot = t.TempDir()
	claude := NewProcessor(t.Context(), cfg, nil, nil, nil, zap.NewNop())
	claude.ShareTurnBudget(codex)
	runner, err := NewRunner(cfg, workerprotocol.NewClient("http://localhost", "", 0), codex, zap.NewNop())
	require.NoError(t, err)
	require.Equal(t, codex.turnSlots, claude.turnSlots)
	require.Equal(t, codex.turnSlots, runner.turnSlots)
	codex.turnSlots <- struct{}{}
	claude.turnSlots <- struct{}{}
	select {
	case runner.turnSlots <- struct{}{}:
		t.Fatal("第三个任务突破共享并发上限")
	default:
	}
	controller := NewHostDesktopController(claude, nil)
	state := &hostCallState{releaseSlot: func() { <-claude.turnSlots }}
	controller.finishHostCall("thread", state)
	controller.finishHostCall("thread", state)
	require.Len(t, codex.turnSlots, 1, "重复终态只能释放一次额度")
}

func TestBrowserTaskIdentityIncludesRuntime(t *testing.T) {
	codex := &Processor{runtimeIdentity: runtimeidentity.Identity{WorkerID: "worker", Engine: runtimeidentity.Codex}}
	claude := &Processor{runtimeIdentity: runtimeidentity.Identity{WorkerID: "worker", Engine: runtimeidentity.Claude}}
	require.NotEqual(t, codex.localBrowserTaskID("same-thread", "same-turn"), claude.localBrowserTaskID("same-thread", "same-turn"))
}
