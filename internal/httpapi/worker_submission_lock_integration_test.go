//go:build integration

package httpapi

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
)

// 首次 Discord 建帖持有 Control 时，提交/确认/终态不得先持有 intent 造成反向等待。
func TestWorkerSubmissionLocksControlBeforeIntent(t *testing.T) {
	for _, operation := range []string{"submit", "confirm", "complete", "reconcile"} {
		t.Run(operation, func(t *testing.T) {
			f := newSessionRuntimeFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			task := f.startRun(t, runtimeidentity.Codex)
			tx, err := f.db.BeginTx(ctx, nil)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback() }()
			var backendPID int
			var locked uuid.UUID
			require.NoError(t, tx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&backendPID))
			require.NoError(t, tx.QueryRowContext(ctx, "SELECT id FROM codex_thread_controls WHERE id=$1 FOR UPDATE",
				task.Claimed.ControlID).Scan(&locked))
			done := make(chan error, 1)
			go func() {
				repository := codexcontrol.NewRepository(f.db, time.Minute)
				switch operation {
				case "submit":
					done <- repository.RecordSubmission(ctx, &task.Claimed, "submission")
				case "confirm":
					done <- repository.ConfirmTurn(ctx, &task.Claimed, "turn")
				case "complete":
					done <- repository.Complete(ctx, &task.Claimed, codexcontrol.TurnResult{TurnID: "turn", FinalAnswer: "done"})
				case "reconcile":
					done <- repository.Reconcile(ctx, &task.Claimed, "test", nil)
				}
			}()
			require.Eventually(t, func() bool {
				var waiting bool
				query := "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))"
				return f.db.QueryRowContext(ctx, query, backendPID).Scan(&waiting) == nil && waiting
			}, 3*time.Second, 10*time.Millisecond, "提交事务必须已到达真实锁等待")
			// 旧实现此处返回 55P03：它先锁住 intent，再等待本事务的 Control。
			require.NoError(t, tx.QueryRowContext(ctx, "SELECT id FROM codex_turn_intents WHERE id=$1 FOR UPDATE NOWAIT",
				task.Claimed.ID).Scan(&locked), "等待 Control 时不能抢占 intent")
			require.NoError(t, tx.Commit())
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if operation == "submit" || operation == "confirm" {
				require.NoError(t, f.clients[runtimeidentity.Codex].Complete(ctx, task,
					codexcontrol.TurnResult{TurnID: "turn", FinalAnswer: "done"}))
			}
		})
	}
}

// 回合终态写入 Session 消息序号，必须与标题/metadata 一样先锁 Session，再锁 Control。
func TestWorkerCompletionLocksSessionBeforeControl(t *testing.T) {
	for _, engine := range []runtimeidentity.Engine{runtimeidentity.Codex, runtimeidentity.Claude} {
		t.Run(string(engine), func(t *testing.T) {
			f := newSessionRuntimeFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			task := f.startRun(t, engine)
			tx, err := f.db.BeginTx(ctx, nil)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback() }()
			var backendPID int
			var locked uuid.UUID
			require.NoError(t, tx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&backendPID))
			require.NoError(t, tx.QueryRowContext(ctx, `SELECT id FROM workspace_sessions
				WHERE id=$1 FOR NO KEY UPDATE`, task.Claimed.SessionID).Scan(&locked))
			done := make(chan error, 1)
			go func() {
				done <- f.clients[engine].Complete(ctx, task,
					codexcontrol.TurnResult{TurnID: "turn", FinalAnswer: "完成且只写入一次"})
			}()
			require.Eventually(t, func() bool {
				var waiting bool
				return f.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
					WHERE $1=ANY(pg_blocking_pids(pid)))`, backendPID).Scan(&waiting) == nil && waiting
			}, 3*time.Second, 10*time.Millisecond, "真实完成 API 必须等待 Session")
			require.NoError(t, tx.QueryRowContext(ctx, `SELECT id FROM codex_thread_controls
				WHERE id=$1 FOR UPDATE NOWAIT`, task.Claimed.ControlID).Scan(&locked),
				"回合完成等待 Session 时不能抢占 Control，避免 metadata 反向等待")
			require.NoError(t, tx.Commit())
			select {
			case result := <-done:
				require.NoError(t, result)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			var replies int
			require.NoError(t, f.db.QueryRowContext(ctx, `SELECT count(*) FROM session_messages
				WHERE session_id=$1 AND message_role='agent'`, task.Claimed.SessionID).Scan(&replies))
			require.Equal(t, 1, replies)
		})
	}
}
