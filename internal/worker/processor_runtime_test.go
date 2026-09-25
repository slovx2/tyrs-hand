package worker

import (
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

func TestProcessorRejectsMismatchedEngineBeforeOpeningRuntime(t *testing.T) {
	// 不提供模型客户端、工作目录或工具；拒绝路径不能触发任何执行准备。
	processor := &Processor{runtimeIdentity: runtimeidentity.Identity{WorkerID: "worker", Engine: runtimeidentity.Codex}}
	for _, engine := range []runtimeidentity.Engine{runtimeidentity.Claude, "", "unknown"} {
		task := &workerprotocol.Task{Snapshot: workerprotocol.TaskSnapshot{
			Runtime: workerprotocol.RuntimeSnapshot{Engine: engine, Model: "gpt-test"},
		}}
		task.Claimed.SourceType = codexcontrol.SourceWorkspace
		_, err := processor.Process(t.Context(), task, nil, nil)
		require.Error(t, err)
	}
	processor.runtimeIdentity.Engine = runtimeidentity.Claude
	task := &workerprotocol.Task{Snapshot: workerprotocol.TaskSnapshot{Runtime: workerprotocol.RuntimeSnapshot{Engine: runtimeidentity.Claude}}}
	task.Claimed.SourceType = codexcontrol.SourceGitHub
	_, err := processor.Process(t.Context(), task, nil, nil)
	require.ErrorContains(t, err, "GitHub")
}
