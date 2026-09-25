//go:build integration

package scheduledtasks

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
)

func TestScheduledTaskRuntimeInheritanceAndIsolation(t *testing.T) {
	db := scheduledTaskDatabase(t)
	ctx := t.Context()
	require.NoError(t, database.Migrate(ctx, db))
	codex := seedScheduledFixture(t, db, "runtime")
	claude := codex
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO workspace_sessions(
		workspace_id,workspace_project_id,agent_profile_id,title,engine,model)
		VALUES ($1,$2,$3,'Claude','claude-code','claude-test') RETURNING id`,
		codex.workspace, codex.project, codex.agent).Scan(&claude.session))
	fixtures := map[runtimeidentity.Engine]scheduledFixture{
		runtimeidentity.Codex: codex, runtimeidentity.Claude: claude,
	}
	service := NewService(db, time.Minute, 5, 3)
	name, prompt := "同名任务", "本地自动化检查"
	schedule := "DTSTART:" + utcScheduleTime(time.Now().Add(time.Hour)) + "\nRRULE:FREQ=HOURLY;INTERVAL=1"
	tasks := map[runtimeidentity.Engine]Task{}
	for engine, fixture := range fixtures {
		task, err := service.Create(ctx, fixture.tool("same-call"), ToolArguments{
			Kind: KindStandalone, Name: &name, Prompt: &prompt, Schedule: &schedule,
		})
		require.NoError(t, err)
		require.Equal(t, engine, task.Engine)
		tasks[engine] = task
		beat, err := service.Create(ctx, fixture.tool("heartbeat"), ToolArguments{
			Kind: KindHeartbeat, Name: &name, Prompt: &prompt, Schedule: &schedule,
		})
		require.NoError(t, err)
		require.Equal(t, engine, beat.Engine)
		run, _, err := service.RunNow(ctx, fixture.tool("heartbeat-run"), beat.ID)
		require.NoError(t, err)
		require.Equal(t, fixture.session, *run.SessionID)
		var snapshot struct {
			Runtime runtimeSettingsSnapshot `json:"runtime"`
		}
		require.NoError(t, json.Unmarshal(run.TaskSnapshot, &snapshot))
		require.Equal(t, engine, snapshot.Runtime.Engine)
	}
	for engine, fixture := range fixtures {
		listed, err := service.List(ctx, fixture.tool("list"), false)
		require.NoError(t, err)
		require.Len(t, listed, 2)
		for _, task := range listed {
			require.Equal(t, engine, task.Engine)
		}
		other := tasks[runtimeidentity.Codex]
		if engine == runtimeidentity.Codex {
			other = tasks[runtimeidentity.Claude]
		}
		_, err = service.Update(ctx, fixture.tool("update"), ToolArguments{TaskID: &other.ID, Name: &name})
		require.Error(t, err)
		_, err = service.Delete(ctx, fixture.tool("delete"), other.ID)
		require.Error(t, err)
		_, _, err = service.RunNow(ctx, fixture.tool("wrong-run"), other.ID)
		require.Error(t, err)
	}
	// 模拟 Control 重启后的调度器：从持久化任务读取引擎，而非来源客户端内存。
	service = NewService(db, time.Minute, 5, 3)
	for engine, task := range tasks {
		_, err := db.ExecContext(ctx, `UPDATE scheduled_tasks SET next_run_at=now()-interval '1 second' WHERE id=$1`, task.ID)
		require.NoError(t, err)
		found, err := service.MaterializeDueWorker(ctx, codex.workerID)
		require.NoError(t, err)
		require.True(t, found)
		var sessionEngine, controlEngine runtimeidentity.Engine
		var snapshot []byte
		require.NoError(t, db.QueryRowContext(ctx, `SELECT session.engine,control.engine,run.task_snapshot
			FROM scheduled_task_runs run JOIN workspace_sessions session ON session.id=run.session_id
			JOIN codex_thread_controls control ON control.session_id=session.id
			WHERE run.scheduled_task_id=$1`, task.ID).Scan(&sessionEngine, &controlEngine, &snapshot))
		require.Equal(t, engine, sessionEngine)
		require.Equal(t, engine, controlEngine)
		var recorded struct {
			Task    Task                    `json:"task"`
			Runtime runtimeSettingsSnapshot `json:"runtime"`
		}
		require.NoError(t, json.Unmarshal(snapshot, &recorded))
		require.Equal(t, engine, recorded.Task.Engine)
		require.Equal(t, engine, recorded.Runtime.Engine)
	}
	_, err := db.ExecContext(ctx, `UPDATE scheduled_tasks SET engine='codex' WHERE engine='claude-code'`)
	require.Error(t, err, "任务引擎不能被后续设置覆盖")
}
