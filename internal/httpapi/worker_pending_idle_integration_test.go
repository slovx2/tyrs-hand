//go:build integration

package httpapi

import (
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

// 真实 PostgreSQL 与 Worker HTTP API 验证选队语义；实际 CLI 副作用由 bootstrap 专项覆盖。
func TestWorkerPendingInputSkipsBusyStartWhenIdle(t *testing.T) {
	f := newSessionRuntimeFixture(t)
	ctx := t.Context()
	client := f.clients[runtimeidentity.Claude]
	busy := f.createThread(t, runtimeidentity.Claude, "a", "busy-thread")
	idle := f.createThread(t, runtimeidentity.Claude, "b", "idle-thread")
	repository := codexcontrol.NewRepository(f.db, time.Minute)
	enqueue := func(control uuid.UUID, key, behavior, operation string) uuid.UUID {
		var session uuid.UUID
		require.NoError(t, f.db.QueryRowContext(ctx, `SELECT session_id FROM codex_thread_controls WHERE id=$1`, control).Scan(&session))
		tx, err := f.db.BeginTx(ctx, nil)
		require.NoError(t, err)
		input, inserted, err := repository.Enqueue(ctx, tx, codexcontrol.EnqueueRequest{
			SourceType: codexcontrol.SourceWorkspace, SessionID: session, InputSurface: "client",
			IdempotencyKey: key, Instruction: key, Behavior: behavior, Operation: operation, ReplyPolicy: "silent",
		})
		require.NoError(t, err)
		require.True(t, inserted)
		require.NoError(t, tx.Commit())
		return input
	}
	initial := enqueue(busy.ControlID, "initial", "start_when_idle", "turn_input")
	claim, err := client.Claim(ctx, workerprotocol.ClaimRequest{Role: "discord"})
	require.NoError(t, err)
	require.NotNil(t, claim.Task)
	require.Equal(t, initial, claim.Task.Claimed.ID)
	claim.Task.Claimed.RunID = uuid.New()
	require.NoError(t, client.DecideInput(ctx, claim.Task, "start", ""))
	waiting := enqueue(busy.ControlID, "waiting", "start_when_idle", "turn_input")
	steer := enqueue(busy.ControlID, "steer", "steer_if_active", "turn_input")
	other := enqueue(idle.ControlID, "other", "start_when_idle", "turn_input")
	selection := workerprotocol.ClaimRequest{Role: "discord", OnlyActive: true, ActiveControlIDs: []uuid.UUID{busy.ControlID}}
	next, err := client.Claim(ctx, selection)
	require.NoError(t, err)
	require.NotNil(t, next.Task)
	require.Equal(t, steer, next.Task.Claimed.ID, "最早的待空闲输入不能阻塞后续可追加输入")
	next.Task.Claimed.RunID = claim.Task.Claimed.RunID
	require.NoError(t, client.DecideInput(ctx, next.Task, "steer", "native-busy-turn"))
	full, err := client.Claim(ctx, selection)
	require.NoError(t, err)
	require.Nil(t, full.Task, "满载时不可启动空闲会话，也不可追加等待空闲的输入")
	selection.OnlyActive = false
	available, err := client.Claim(ctx, selection)
	require.NoError(t, err)
	require.NotNil(t, available.Task)
	require.Equal(t, other, available.Task.Claimed.ID, "有空槽时可以启动其他会话，不能被活动会话排队输入挡住")
	stop := enqueue(busy.ControlID, "stop", "start_when_idle", "interrupt")
	stopping, err := client.Claim(ctx, selection)
	require.NoError(t, err)
	require.NotNil(t, stopping.Task)
	require.Equal(t, stop, stopping.Task.Claimed.ID, "停止操作必须优先，不能被同名等待空闲策略过滤")
	stopping.Task.Claimed.RunID = claim.Task.Claimed.RunID
	require.NoError(t, client.DecideInput(ctx, stopping.Task, "interrupt", "native-busy-turn"))
	var status string
	var action sql.NullString
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT status,resolved_action FROM codex_turn_intents WHERE id=$1`, waiting).Scan(&status, &action))
	require.Equal(t, "queued", status)
	require.False(t, action.Valid, "过滤只影响候选选择，不得消耗或丢弃输入")
	require.NoError(t, client.Complete(ctx, claim.Task, codexcontrol.TurnResult{TurnID: "native-busy-turn", FinalAnswer: "done"}))
	selection.ActiveControlIDs = nil
	released, err := client.Claim(ctx, selection)
	require.NoError(t, err)
	require.NotNil(t, released.Task)
	require.Equal(t, waiting, released.Task.Claimed.ID, "旧回合结束后按原顺序返回同一排队输入")
	foreign, err := f.clients[runtimeidentity.Codex].Claim(ctx, selection)
	require.NoError(t, err)
	require.Nil(t, foreign.Task, "另一引擎不得领取排队输入")
	replacement := enqueue(idle.ControlID, "replace", "start_when_idle", "replace_last_turn")
	selection.ActiveControlIDs = []uuid.UUID{idle.ControlID}
	selection.OnlyActive = true
	replacing, err := client.Claim(ctx, selection)
	require.NoError(t, err)
	require.NotNil(t, replacing.Task)
	require.Equal(t, replacement, replacing.Task.Claimed.ID, "替换操作必须优先，不能被同名等待空闲策略过滤")
}
