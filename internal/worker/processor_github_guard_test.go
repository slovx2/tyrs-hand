package worker

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

// GitHub 已停用；即使遗留任务被错误投递到 Claude 执行器，也只能在任何 Control 或原生调用前拒绝。
func TestProcessorRejectsGitHubTaskForClaudeBeforeAnyAction(t *testing.T) {
	processor := &Processor{}
	processor.runtimeIdentity.Engine = runtimeidentity.Claude
	task := &workerprotocol.Task{}
	task.Snapshot.Runtime.Engine = runtimeidentity.Claude
	task.Claimed.SourceType = codexcontrol.SourceGitHub
	reported := 0
	_, err := processor.Process(context.Background(), task, nil, func(string, json.RawMessage) { reported++ })
	require.EqualError(t, err, "GitHub 任务仅支持 Codex 运行时")
	require.Zero(t, reported, "拒绝前不能上报任何事件")
}
