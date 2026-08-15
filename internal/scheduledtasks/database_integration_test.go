//go:build integration

package scheduledtasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

type scheduledFixture struct {
	workerID  uuid.UUID
	workspace uuid.UUID
	project   uuid.UUID
	session   uuid.UUID
	agent     uuid.UUID
}

func TestScheduledTasksDatabaseIntegration(t *testing.T) {
	db := scheduledTaskDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))

	t.Run("CRUD、Workspace 隔离和 run_now", func(t *testing.T) {
		first := seedScheduledFixture(t, db, "crud-a")
		second := seedScheduledFixture(t, db, "crud-b")
		service := NewService(db, 2*time.Second, 5, 3)
		clock := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
		service.now = func() time.Time { return clock }
		name, prompt := "日报", "总结项目状态"
		scheduleText := "DTSTART:20300102T000000Z\nRRULE:FREQ=DAILY"
		task, err := service.Create(ctx, first.tool("create"), ToolArguments{
			Action: "create", Kind: KindStandalone, Name: &name, Prompt: &prompt,
			Schedule: &scheduleText,
		})
		require.NoError(t, err)
		require.Equal(t, StatusActive, task.Status)
		require.Equal(t, int64(1), task.ScheduleRevision)
		require.NotNil(t, task.NextRunAt)
		require.Equal(t, first.workspace, task.WorkspaceID)
		require.Nil(t, task.CreatedByAdministratorID)

		listed, err := service.List(ctx, first.tool("list"), false)
		require.NoError(t, err)
		require.Len(t, listed, 1)
		listed, err = service.List(ctx, second.tool("list"), false)
		require.NoError(t, err)
		require.Empty(t, listed)
		_, err = service.Update(ctx, second.tool("update-other"), ToolArguments{
			TaskID: &task.ID, Name: &name,
		})
		require.ErrorContains(t, err, "不属于当前 Workspace")
		_, err = service.Update(ctx, first.tool("change-kind"), ToolArguments{
			TaskID: &task.ID, Kind: KindHeartbeat,
		})
		require.ErrorContains(t, err, "不可修改")

		paused := StatusPaused
		updatedName := "每日项目日报"
		task, err = service.Update(ctx, first.tool("pause"), ToolArguments{
			TaskID: &task.ID, Name: &updatedName, Status: &paused,
		})
		require.NoError(t, err)
		require.Equal(t, StatusPaused, task.Status)
		require.Nil(t, task.NextRunAt)
		require.Equal(t, int64(2), task.ScheduleRevision)

		active := StatusActive
		task, err = service.Update(ctx, first.tool("resume"), ToolArguments{
			TaskID: &task.ID, Status: &active,
		})
		require.NoError(t, err)
		require.NotNil(t, task.NextRunAt)
		nextBeforeRunNow := *task.NextRunAt
		run, deduplicated, err := service.RunNow(ctx, first.tool("run-now-1"), task.ID)
		require.NoError(t, err)
		require.False(t, deduplicated)
		require.Equal(t, "run_now", run.Trigger)
		runAgain, deduplicated, err := service.RunNow(ctx, first.tool("run-now-2"), task.ID)
		require.NoError(t, err)
		require.True(t, deduplicated)
		require.Equal(t, run.ID, runAgain.ID)
		task, err = service.taskForSession(ctx, task.ID, first.session, false)
		require.NoError(t, err)
		require.Equal(t, nextBeforeRunNow, *task.NextRunAt)

		task, err = service.Delete(ctx, first.tool("delete"), task.ID)
		require.NoError(t, err)
		require.Equal(t, StatusDeleted, task.Status)
		currentRun, err := scanRun(db.QueryRowContext(ctx, `SELECT `+runColumns+`
			FROM scheduled_task_runs WHERE id=$1`, run.ID))
		require.NoError(t, err)
		require.Equal(t, "queued", currentRun.Status)
		listed, err = service.List(ctx, first.tool("list"), false)
		require.NoError(t, err)
		require.Empty(t, listed)
		listed, err = service.List(ctx, first.tool("list-deleted"), true)
		require.NoError(t, err)
		require.Len(t, listed, 1)
	})

	t.Run("heartbeat 唯一约束", func(t *testing.T) {
		fixture := seedScheduledFixture(t, db, "heartbeat-unique")
		service := NewService(db, time.Minute, 5, 3)
		service.now = func() time.Time {
			return time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
		}
		name, prompt := "跟进", "继续检查"
		raw := "DTSTART:20300102T000000Z\nRRULE:FREQ=DAILY"
		first, err := service.Create(ctx, fixture.tool("heartbeat-1"), ToolArguments{
			Kind: KindHeartbeat, Name: &name, Prompt: &prompt, Schedule: &raw,
		})
		require.NoError(t, err)
		_, err = service.Create(ctx, fixture.tool("heartbeat-2"), ToolArguments{
			Kind: KindHeartbeat, Name: &name, Prompt: &prompt, Schedule: &raw,
		})
		require.Error(t, err)

		paused := StatusPaused
		_, err = service.Update(ctx, fixture.tool("pause-heartbeat"), ToolArguments{
			TaskID: &first.ID, Status: &paused,
		})
		require.NoError(t, err)
		second, err := service.Create(ctx, fixture.tool("heartbeat-3"), ToolArguments{
			Kind: KindHeartbeat, Name: &name, Prompt: &prompt, Schedule: &raw,
		})
		require.NoError(t, err)
		active := StatusActive
		_, err = service.Update(ctx, fixture.tool("resume-heartbeat"), ToolArguments{
			TaskID: &first.ID, Status: &active,
		})
		require.Error(t, err)
		require.NotEqual(t, first.ID, second.ID)
	})

	t.Run("standalone 物化与 Intent 生命周期", func(t *testing.T) {
		fixture := seedScheduledFixture(t, db, "standalone")
		service := NewService(db, 2*time.Second, 5, 3)
		occurrence := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
		creationClock := occurrence.Add(-time.Hour)
		service.now = func() time.Time { return creationClock }
		name, prompt := "一次巡检", "检查服务并报告"
		raw := "DTSTART:" + utcScheduleTime(occurrence)
		model := "unavailable-test-model"
		task, err := service.Create(ctx, fixture.tool("standalone-create"), ToolArguments{
			Kind: KindStandalone, Name: &name, Prompt: &prompt, Schedule: &raw,
			Settings: &SettingsInput{Model: &model},
		})
		require.NoError(t, err)
		service.now = time.Now

		materialized, err := service.MaterializeDueWorker(ctx, fixture.workerID)
		require.NoError(t, err)
		require.True(t, materialized)
		materialized, err = service.MaterializeDueWorker(ctx, fixture.workerID)
		require.NoError(t, err)
		require.False(t, materialized)

		task, err = service.taskForSession(ctx, task.ID, fixture.session, false)
		require.NoError(t, err)
		require.Equal(t, StatusCompleted, task.Status)
		run, err := scanRun(db.QueryRowContext(ctx, `SELECT `+runColumns+`
			FROM scheduled_task_runs WHERE scheduled_task_id=$1`, task.ID))
		require.NoError(t, err)
		require.NotEqual(t, fixture.session, *run.SessionID)
		require.Contains(t, string(run.TaskSnapshot), model)
		var title, titleSource string
		var sessionModel sql.NullString
		require.NoError(t, db.QueryRowContext(ctx, `SELECT title,title_source,model
			FROM workspace_sessions WHERE id=$1`, *run.SessionID).
			Scan(&title, &titleSource, &sessionModel))
		require.Contains(t, title, "定时任务 · 一次巡检")
		require.Equal(t, "manual", titleSource)
		require.Equal(t, model, sessionModel.String)
		var desiredTitle, desiredTitleSource string
		var desiredTitleRevision int64
		require.NoError(t, db.QueryRowContext(ctx, `SELECT desired_thread_name,
			desired_thread_name_source,desired_thread_name_revision
			FROM codex_thread_controls WHERE session_id=$1`, *run.SessionID).
			Scan(&desiredTitle, &desiredTitleSource, &desiredTitleRevision))
		require.Equal(t, title, desiredTitle)
		require.Equal(t, "fallback", desiredTitleSource)
		require.Equal(t, int64(1), desiredTitleRevision)

		var instruction, messageContent string
		require.NoError(t, db.QueryRowContext(ctx, `SELECT instruction
			FROM codex_turn_intents WHERE id=$1`, *run.IntentID).Scan(&instruction))
		require.Contains(t, instruction, "<scheduled_task>")
		require.Contains(t, instruction, task.ID.String())
		require.NoError(t, db.QueryRowContext(ctx, `SELECT content::text FROM session_messages
			WHERE session_id=$1 AND message_role='user'`, *run.SessionID).Scan(&messageContent))
		require.Contains(t, messageContent, prompt)

		repository := codexcontrol.NewRepository(db, 2*time.Second, 5, 3)
		claimed, err := repository.ClaimWorker(ctx, fixture.workerID.String(),
			codexcontrol.SourceWorkspace, fixture.workerID)
		require.NoError(t, err)
		require.NotNil(t, claimed)
		run, err = scanRun(db.QueryRowContext(ctx, `SELECT `+runColumns+`
			FROM scheduled_task_runs WHERE id=$1`, run.ID))
		require.NoError(t, err)
		require.Equal(t, "running", run.Status)
		require.NoError(t, repository.Complete(ctx, claimed, codexcontrol.TurnResult{
			FinalAnswer: "巡检完成", TurnID: "scheduled-turn",
		}))
		run, err = scanRun(db.QueryRowContext(ctx, `SELECT `+runColumns+`
			FROM scheduled_task_runs WHERE id=$1`, run.ID))
		require.NoError(t, err)
		require.Equal(t, "succeeded", run.Status)
		var agentMessages int
		require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM session_messages
			WHERE session_id=$1 AND message_role='agent'`, *run.SessionID).Scan(&agentMessages))
		require.Equal(t, 1, agentMessages)
	})

	t.Run("终态失败可审查且任务不自动暂停", func(t *testing.T) {
		fixture := seedScheduledFixture(t, db, "terminal-failure")
		service := NewService(db, 2*time.Second, 5, 3)
		occurrence := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
		service.now = func() time.Time { return occurrence.Add(-time.Hour) }
		name, prompt := "失败巡检", "执行可能失败的巡检"
		raw := "DTSTART:" + utcScheduleTime(occurrence) + "\nRRULE:FREQ=HOURLY"
		task, err := service.Create(ctx, fixture.tool("failure-create"), ToolArguments{
			Kind: KindStandalone, Name: &name, Prompt: &prompt, Schedule: &raw,
		})
		require.NoError(t, err)
		service.now = time.Now
		materialized, err := service.MaterializeDueWorker(ctx, fixture.workerID)
		require.NoError(t, err)
		require.True(t, materialized)
		repository := codexcontrol.NewRepository(db, 2*time.Second, 5, 3)
		claimed, err := repository.ClaimWorker(ctx, fixture.workerID.String(),
			codexcontrol.SourceWorkspace, fixture.workerID)
		require.NoError(t, err)
		require.NotNil(t, claimed)
		firstAttemptRun := claimed.RunID
		require.NoError(t, repository.Reconcile(ctx, claimed, "temporary_failure",
			errors.New("临时失败")))
		var scheduledRunCount int
		require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM scheduled_task_runs
			WHERE scheduled_task_id=$1`, task.ID).Scan(&scheduledRunCount))
		require.Equal(t, 1, scheduledRunCount)
		_, err = db.ExecContext(ctx, `UPDATE codex_turn_intents SET available_at=now()
			WHERE id=$1`, claimed.ID)
		require.NoError(t, err)
		claimed, err = repository.ClaimWorker(ctx, fixture.workerID.String(),
			codexcontrol.SourceWorkspace, fixture.workerID)
		require.NoError(t, err)
		require.NotNil(t, claimed)
		require.NotEqual(t, firstAttemptRun, claimed.RunID)
		claimed.Attempt = claimed.MaxAttempts
		require.NoError(t, repository.Reconcile(ctx, claimed, "model_unavailable",
			errors.New("模型当前不可用")))

		task, err = service.taskForSession(ctx, task.ID, fixture.session, false)
		require.NoError(t, err)
		require.Equal(t, StatusActive, task.Status)
		require.NotNil(t, task.NextRunAt)
		require.Equal(t, "model_unavailable", *task.LastErrorCode)
		run, err := scanRun(db.QueryRowContext(ctx, `SELECT `+runColumns+`
			FROM scheduled_task_runs WHERE scheduled_task_id=$1`, task.ID))
		require.NoError(t, err)
		require.Equal(t, "failed", run.Status)
		require.Equal(t, "model_unavailable", *run.ErrorCode)
		var failureUpdates int
		require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM client_updates
			WHERE session_id=$1 AND update_type='run.failed'`, *run.SessionID).
			Scan(&failureUpdates))
		require.Equal(t, 1, failureUpdates)
	})

	t.Run("heartbeat 原 Session、overlap 和归档状态", func(t *testing.T) {
		fixture := seedScheduledFixture(t, db, "heartbeat-lifecycle")
		conversationID := bindScheduledDiscordConversation(t, db, fixture)
		tx, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)
		priorIntent, inserted, err := codexcontrol.NewRepository(db, time.Minute).Enqueue(ctx, tx,
			codexcontrol.EnqueueRequest{SourceType: codexcontrol.SourceWorkspace,
				SessionID: fixture.session, DiscordConversationID: conversationID,
				IdempotencyKey: "prior-discord-" + uuid.NewString(), Instruction: "prior",
				ReplyPolicy: "silent"})
		require.NoError(t, err)
		require.True(t, inserted)
		require.NoError(t, tx.Commit())
		_, err = db.ExecContext(ctx, `UPDATE codex_turn_intents SET status='completed'
			WHERE id=$1`, priorIntent)
		require.NoError(t, err)
		service := NewService(db, 2*time.Second, 5, 3)
		occurrence := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
		creationClock := occurrence.Add(-time.Hour)
		service.now = func() time.Time { return creationClock }
		name, prompt := "会话心跳", "继续当前会话"
		raw := "DTSTART:" + utcScheduleTime(occurrence)
		task, err := service.Create(ctx, fixture.tool("heartbeat-create"), ToolArguments{
			Kind: KindHeartbeat, Name: &name, Prompt: &prompt, Schedule: &raw,
		})
		require.NoError(t, err)
		service.now = time.Now

		_, err = db.ExecContext(ctx, `UPDATE workspace_sessions
			SET lifecycle_state='archive_pending' WHERE id=$1`, fixture.session)
		require.NoError(t, err)
		materialized, err := service.MaterializeDueWorker(ctx, fixture.workerID)
		require.NoError(t, err)
		require.True(t, materialized)
		task, err = service.taskForSession(ctx, task.ID, fixture.session, false)
		require.NoError(t, err)
		require.Equal(t, StatusActive, task.Status)
		require.NotNil(t, task.BlockedUntil)

		_, err = db.ExecContext(ctx, `UPDATE workspace_sessions SET lifecycle_state='active'
			WHERE id=$1`, fixture.session)
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, `UPDATE scheduled_tasks SET blocked_until=NULL WHERE id=$1`,
			task.ID)
		require.NoError(t, err)
		materialized, err = service.MaterializeDueWorker(ctx, fixture.workerID)
		require.NoError(t, err)
		require.True(t, materialized)
		run, err := scanRun(db.QueryRowContext(ctx, `SELECT `+runColumns+`
			FROM scheduled_task_runs WHERE scheduled_task_id=$1`, task.ID))
		require.NoError(t, err)
		require.Equal(t, fixture.session, *run.SessionID)
		var scheduledConversation sql.NullString
		require.NoError(t, db.QueryRowContext(ctx, `SELECT discord_conversation_id::text
			FROM codex_turn_intents WHERE id=$1`, *run.IntentID).Scan(&scheduledConversation))
		require.False(t, scheduledConversation.Valid, "定时任务不应自动产生 Discord 投影")

		_, err = db.ExecContext(ctx, `UPDATE scheduled_tasks SET status='active',
			next_run_at=$2,blocked_until=NULL WHERE id=$1`, task.ID, occurrence)
		require.NoError(t, err)
		materialized, err = service.MaterializeDueWorker(ctx, fixture.workerID)
		require.NoError(t, err)
		require.True(t, materialized)
		var runCount int
		require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM scheduled_task_runs
			WHERE scheduled_task_id=$1`, task.ID).Scan(&runCount))
		require.Equal(t, 1, runCount)
		run, err = scanRun(db.QueryRowContext(ctx, `SELECT `+runColumns+`
			FROM scheduled_task_runs WHERE id=$1`, run.ID))
		require.NoError(t, err)
		require.NotNil(t, run.CoalescedThrough)

		_, err = db.ExecContext(ctx, `UPDATE codex_turn_intents
			SET status='waiting_for_user' WHERE id=$1`, *run.IntentID)
		require.NoError(t, err)
		run, err = scanRun(db.QueryRowContext(ctx, `SELECT `+runColumns+`
			FROM scheduled_task_runs WHERE id=$1`, run.ID))
		require.NoError(t, err)
		require.Equal(t, "waiting_for_user", run.Status)
		_, err = db.ExecContext(ctx, `UPDATE codex_turn_intents SET status='retry_wait'
			WHERE id=$1`, *run.IntentID)
		require.NoError(t, err)
		run, err = scanRun(db.QueryRowContext(ctx, `SELECT `+runColumns+`
			FROM scheduled_task_runs WHERE id=$1`, run.ID))
		require.NoError(t, err)
		require.Equal(t, "queued", run.Status)
		_, err = db.ExecContext(ctx, `UPDATE codex_turn_intents SET status='failed',
			last_error_code='test_failure',last_error_message='test failed' WHERE id=$1`,
			*run.IntentID)
		require.NoError(t, err)
		run, err = scanRun(db.QueryRowContext(ctx, `SELECT `+runColumns+`
			FROM scheduled_task_runs WHERE id=$1`, run.ID))
		require.NoError(t, err)
		require.Equal(t, "failed", run.Status)
		require.Equal(t, "test_failure", *run.ErrorCode)

		service.now = func() time.Time { return creationClock }
		archivedTask, err := service.Create(ctx, fixture.tool("archived-create"), ToolArguments{
			Kind: KindHeartbeat, Name: &name, Prompt: &prompt, Schedule: &raw,
		})
		require.NoError(t, err)
		service.now = time.Now
		_, err = db.ExecContext(ctx, `UPDATE workspace_sessions SET lifecycle_state='archived'
			WHERE id=$1`, fixture.session)
		require.NoError(t, err)
		materialized, err = service.MaterializeDueWorker(ctx, fixture.workerID)
		require.NoError(t, err)
		require.True(t, materialized)
		archivedTask, err = service.taskForSession(ctx, archivedTask.ID, fixture.session, false)
		require.NoError(t, err)
		require.Equal(t, StatusPaused, archivedTask.Status)
		require.Equal(t, "target_session_inactive", *archivedTask.LastErrorCode)
	})

	t.Run("interval cooldown", func(t *testing.T) {
		fixture := seedScheduledFixture(t, db, "cooldown")
		service := NewService(db, time.Minute, 5, 3)
		occurrence := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Second)
		service.now = func() time.Time { return occurrence.Add(-time.Hour) }
		name, prompt := "轮询", "检查进度"
		raw := "DTSTART:" + utcScheduleTime(occurrence) + "\nRRULE:FREQ=MINUTELY"
		task, err := service.Create(ctx, fixture.tool("cooldown-create"), ToolArguments{
			Kind: KindHeartbeat, Name: &name, Prompt: &prompt, Schedule: &raw,
		})
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, `UPDATE workspace_sessions SET last_activity_at=now()
			WHERE id=$1`, fixture.session)
		require.NoError(t, err)
		service.now = time.Now
		materialized, err := service.MaterializeDueWorker(ctx, fixture.workerID)
		require.NoError(t, err)
		require.True(t, materialized)
		task, err = service.taskForSession(ctx, task.ID, fixture.session, false)
		require.NoError(t, err)
		require.NotNil(t, task.BlockedUntil)
		require.True(t, task.BlockedUntil.After(time.Now()))
		var runCount int
		require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM scheduled_task_runs
			WHERE scheduled_task_id=$1`, task.ID).Scan(&runCount))
		require.Zero(t, runCount)
	})

	t.Run("heartbeat Control 跟随 Workspace 当前 Worker", func(t *testing.T) {
		fixture := seedScheduledFixture(t, db, "heartbeat-worker-move")
		service := NewService(db, time.Minute, 5, 3)
		occurrence := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
		creationClock := occurrence.Add(-time.Hour)
		service.now = func() time.Time { return creationClock }
		name, prompt := "迁移心跳", "继续迁移后的会话"
		raw := "DTSTART:" + utcScheduleTime(occurrence)
		first, err := service.Create(ctx, fixture.tool("move-heartbeat-1"), ToolArguments{
			Kind: KindHeartbeat, Name: &name, Prompt: &prompt, Schedule: &raw,
		})
		require.NoError(t, err)
		service.now = time.Now
		materialized, err := service.MaterializeDueWorker(ctx, fixture.workerID)
		require.NoError(t, err)
		require.True(t, materialized)
		var firstIntent uuid.UUID
		require.NoError(t, db.QueryRowContext(ctx, `SELECT intent_id FROM scheduled_task_runs
			WHERE scheduled_task_id=$1`, first.ID).Scan(&firstIntent))
		_, err = db.ExecContext(ctx, `UPDATE codex_turn_intents SET status='completed'
			WHERE id=$1`, firstIntent)
		require.NoError(t, err)

		var newWorker uuid.UUID
		require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO workers(name,roles,status)
			VALUES ($1,'["discord"]'::jsonb,'online') RETURNING id`,
			"heartbeat-moved-worker-"+uuid.NewString()).Scan(&newWorker))
		_, err = db.ExecContext(ctx, `UPDATE worker_workspaces SET worker_id=$2 WHERE id=$1`,
			fixture.workspace, newWorker)
		require.NoError(t, err)
		service.now = func() time.Time { return creationClock }
		second, err := service.Create(ctx, fixture.tool("move-heartbeat-2"), ToolArguments{
			Kind: KindHeartbeat, Name: &name, Prompt: &prompt, Schedule: &raw,
		})
		require.NoError(t, err)
		service.now = time.Now
		materialized, err = service.MaterializeDueWorker(ctx, newWorker)
		require.NoError(t, err)
		require.True(t, materialized)

		var controlWorker uuid.UUID
		require.NoError(t, db.QueryRowContext(ctx, `SELECT control.worker_id
			FROM codex_thread_controls control
			JOIN scheduled_task_runs run ON run.session_id=control.session_id
			WHERE run.scheduled_task_id=$1`, second.ID).Scan(&controlWorker))
		require.Equal(t, newWorker, controlWorker)
		claimed, err := codexcontrol.NewRepository(db, time.Minute, 5, 3).ClaimWorker(
			ctx, newWorker.String(), codexcontrol.SourceWorkspace, newWorker)
		require.NoError(t, err)
		require.NotNil(t, claimed)
		var secondIntent uuid.UUID
		require.NoError(t, db.QueryRowContext(ctx, `SELECT intent_id FROM scheduled_task_runs
			WHERE scheduled_task_id=$1`, second.ID).Scan(&secondIntent))
		require.Equal(t, secondIntent, claimed.ID)
	})

	t.Run("Workspace 换 Worker、项目恢复和并发物化", func(t *testing.T) {
		fixture := seedScheduledFixture(t, db, "worker-move")
		service := NewService(db, time.Minute, 5, 3)
		occurrence := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
		service.now = func() time.Time { return occurrence.Add(-time.Hour) }
		name, prompt := "迁移巡检", "检查迁移后的项目"
		raw := "DTSTART:" + utcScheduleTime(occurrence)
		task, err := service.Create(ctx, fixture.tool("move-create"), ToolArguments{
			Kind: KindStandalone, Name: &name, Prompt: &prompt, Schedule: &raw,
		})
		require.NoError(t, err)
		service.now = time.Now

		var newWorker uuid.UUID
		require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO workers(name,roles,status)
			VALUES ($1,'["discord"]'::jsonb,'online') RETURNING id`,
			"scheduled-worker-moved-"+uuid.NewString()).Scan(&newWorker))
		_, err = db.ExecContext(ctx, `UPDATE worker_workspaces SET worker_id=$2 WHERE id=$1`,
			fixture.workspace, newWorker)
		require.NoError(t, err)
		materialized, err := service.MaterializeDueWorker(ctx, fixture.workerID)
		require.NoError(t, err)
		require.False(t, materialized)

		_, err = db.ExecContext(ctx, `UPDATE workspace_projects SET availability_status='missing'
			WHERE id=$1`, fixture.project)
		require.NoError(t, err)
		materialized, err = service.MaterializeDueWorker(ctx, newWorker)
		require.NoError(t, err)
		require.True(t, materialized)
		task, err = service.taskForSession(ctx, task.ID, fixture.session, false)
		require.NoError(t, err)
		require.Equal(t, StatusActive, task.Status)
		require.Equal(t, "project_unavailable", *task.LastErrorCode)
		require.NotNil(t, task.BlockedUntil)

		_, err = db.ExecContext(ctx, `UPDATE workspace_projects SET availability_status='available'
			WHERE id=$1`, fixture.project)
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, `UPDATE scheduled_tasks SET blocked_until=NULL WHERE id=$1`,
			task.ID)
		require.NoError(t, err)
		results := make([]bool, 2)
		errorsFound := make([]error, 2)
		var group sync.WaitGroup
		for index := range results {
			group.Add(1)
			go func(index int) {
				defer group.Done()
				materializer := NewService(db, time.Minute, 5, 3)
				results[index], errorsFound[index] = materializer.MaterializeDueWorker(ctx,
					newWorker)
			}(index)
		}
		group.Wait()
		require.NoError(t, errorsFound[0])
		require.NoError(t, errorsFound[1])
		require.NotEqual(t, results[0], results[1])
		var runCount int
		require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM scheduled_task_runs
			WHERE scheduled_task_id=$1`, task.ID).Scan(&runCount))
		require.Equal(t, 1, runCount)
		var coalesced sql.NullTime
		require.NoError(t, db.QueryRowContext(ctx, `SELECT coalesced_through
			FROM scheduled_task_runs WHERE scheduled_task_id=$1`, task.ID).Scan(&coalesced))
		require.True(t, coalesced.Valid)
		var controlWorker uuid.UUID
		require.NoError(t, db.QueryRowContext(ctx, `SELECT worker_id
			FROM codex_thread_controls WHERE session_id=(SELECT session_id
			FROM scheduled_task_runs WHERE scheduled_task_id=$1)`, task.ID).Scan(&controlWorker))
		require.Equal(t, newWorker, controlWorker)
	})
}

func (fixture scheduledFixture) tool(callID string) ToolContext {
	return ToolContext{SessionID: fixture.session, ProjectID: fixture.project,
		AgentProfileID: fixture.agent, CallID: callID}
}

func utcScheduleTime(value time.Time) string {
	return value.UTC().Format("20060102T150405Z")
}

func seedScheduledFixture(t *testing.T, db *sql.DB, suffix string) scheduledFixture {
	t.Helper()
	ctx := context.Background()
	unique := suffix + "-" + uuid.NewString()
	guildID := "scheduled-guild-" + unique
	ownerID := "scheduled-owner-" + unique
	_, err := db.ExecContext(ctx, `INSERT INTO discord_guilds(guild_id,enabled)
		VALUES ($1,true)`, guildID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO discord_members(
		guild_id,discord_user_id,username,display_name)
		VALUES ($1,$2,$3,$4)`, guildID, ownerID, ownerID, "Scheduled Owner")
	require.NoError(t, err)

	var fixture scheduledFixture
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO workers(name,roles,status)
		VALUES ($1,'["discord"]'::jsonb,'online') RETURNING id`,
		"scheduled-worker-"+unique).Scan(&fixture.workerID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO worker_workspaces(
		guild_id,owner_discord_user_id,worker_id) VALUES ($1,$2,$3) RETURNING id`,
		guildID, ownerID, fixture.workerID).Scan(&fixture.workspace))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO workspace_projects(
		workspace_id,relative_path,name,project_kind,availability_status)
		VALUES ($1,$2,$3,'directory','available') RETURNING id`, fixture.workspace,
		"projects/"+unique, "scheduled-"+suffix).Scan(&fixture.project))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id FROM agent_profiles
		WHERE name='Default'`).Scan(&fixture.agent))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO workspace_sessions(
		workspace_id,workspace_project_id,agent_profile_id,title,model,
		reasoning_effort,service_tier,collaboration_mode)
		VALUES ($1,$2,$3,$4,'gpt-test','high','standard','default') RETURNING id`,
		fixture.workspace, fixture.project, fixture.agent, "Session "+suffix).
		Scan(&fixture.session))
	return fixture
}

func bindScheduledDiscordConversation(t *testing.T, db *sql.DB,
	fixture scheduledFixture,
) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var guildID, ownerID string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT guild_id,owner_discord_user_id
		FROM worker_workspaces WHERE id=$1`, fixture.workspace).Scan(&guildID, &ownerID))
	unique := uuid.NewString()
	var resourceID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_resources(
		guild_id,resource_key,discord_id,kind,name,managed_marker)
		VALUES ($1,$2,$3,'forum','scheduled','scheduled-test') RETURNING id`, guildID,
		"scheduled-resource-"+unique, "scheduled-forum-"+unique).Scan(&resourceID))
	var forumID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_forums(
		guild_id,resource_id,forum_type,owner_discord_user_id,workspace_id,
		workspace_project_id) VALUES ($1,$2,'workspace',$3,$4,$5) RETURNING id`,
		guildID, resourceID, ownerID, fixture.workspace, fixture.project).Scan(&forumID))
	var conversationID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_conversations(
		guild_id,forum_id,thread_id,owner_discord_user_id,agent_profile_id,title,
		workspace_project_id,session_id) VALUES ($1,$2,$3,$4,$5,'scheduled',$6,$7)
		RETURNING id`, guildID, forumID, "scheduled-thread-"+unique, ownerID,
		fixture.agent, fixture.project, fixture.session).Scan(&conversationID))
	return conversationID
}

func scheduledTaskDatabase(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: "postgres:18.3-bookworm@sha256:80630f83606d8db77d30b3851b16a9f78be2d0d4dda6f7b82a1fdca5ebe3acba",
			Env: map[string]string{
				"POSTGRES_DB": "tyrs_hand", "POSTGRES_USER": "tyrs_hand",
				"POSTGRES_PASSWORD": "test-password",
			},
			ExposedPorts: []string{"5432/tcp"},
			WaitingFor: wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(90 * time.Second),
		},
		Started: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, testcontainers.TerminateContainer(container)) })
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	for attempt := 0; err != nil && attempt < 50; attempt++ {
		time.Sleep(100 * time.Millisecond)
		port, err = container.MappedPort(ctx, "5432/tcp")
	}
	require.NoError(t, err)
	db, err := database.Open(ctx, fmt.Sprintf(
		"postgres://tyrs_hand:test-password@%s:%s/tyrs_hand?sslmode=disable",
		host, port.Port()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}
