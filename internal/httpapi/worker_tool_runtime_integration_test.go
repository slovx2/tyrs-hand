//go:build integration

package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

func TestWorkerToolRuntimeDeduplicationAndSideEffects(t *testing.T) {
	f := newSessionRuntimeFixture(t)
	ctx := t.Context()
	namespace := "tyrs_hand"
	request := codex.ToolCallRequest{Namespace: &namespace, Tool: "automation_update",
		ThreadID: "same-thread", TurnID: "same-turn", CallID: "same-call",
		Arguments: json.RawMessage(`{"action":"create","kind":"standalone","name":"同名任务","prompt":"本地测试","schedule":"DTSTART:20300102T000000Z\nRRULE:FREQ=DAILY"}`)}
	tasks := map[runtimeidentity.Engine]*workerprotocol.Task{}
	results := map[runtimeidentity.Engine]codex.ToolCallResult{}
	for _, engine := range []runtimeidentity.Engine{runtimeidentity.Codex, runtimeidentity.Claude} {
		task := f.startRun(t, engine)
		tasks[engine] = task
		result, err := f.clients[engine].CallTool(ctx, task, request)
		require.NoError(t, err)
		results[engine] = result
		// 重新创建 HTTP 客户端模拟响应丢失后重连：同一 ID 只创建一个调度记录。
		base := workerprotocol.NewClient(f.endpoint, f.credential, 0)
		reconnected, err := base.ForEngine(engine)
		require.NoError(t, err)
		replay, err := reconnected.CallTool(ctx, task, request)
		require.NoError(t, err)
		require.Equal(t, result, replay)
		var count int
		require.NoError(t, f.db.QueryRowContext(ctx, `SELECT count(*) FROM scheduled_tasks WHERE workspace_id=$1 AND engine=$2`, f.workspaceID, engine).Scan(&count))
		require.Equal(t, 1, count, "检查实际调度记录，不能只验证成功响应")
		changed := request
		changed.Arguments = json.RawMessage(`{"action":"list"}`)
		_, err = reconnected.CallTool(ctx, task, changed)
		assertRuntimeHTTPStatus(t, err, http.StatusForbidden)
	}
	require.NotEqual(t, results[runtimeidentity.Codex], results[runtimeidentity.Claude])
	for engine, task := range tasks {
		other := runtimeidentity.Codex
		if engine == other {
			other = runtimeidentity.Claude
		}
		_, err := f.clients[other].CallTool(ctx, task, request)
		assertRuntimeHTTPStatus(t, err, http.StatusNotFound)
		var controlID uuid.UUID
		require.NoError(t, f.db.QueryRowContext(ctx, `SELECT control_id FROM tool_calls WHERE run_id=$1`, task.Claimed.RunID).Scan(&controlID))
		require.Equal(t, task.Claimed.ControlID, controlID)
	}
	// 已有 Control 绑定也受复合外键保护，不能把执行结果移到另一引擎。
	_, err := f.db.ExecContext(ctx, `UPDATE tool_calls SET control_id=$2 WHERE run_id=$1`,
		tasks[runtimeidentity.Codex].Claimed.RunID, tasks[runtimeidentity.Claude].Claimed.ControlID)
	require.Error(t, err)
}
