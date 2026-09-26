//go:build integration

package bootstrap

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func verifyAutomationWaitsForIdle(t *testing.T, ctx context.Context, f controlRuntimeFixture, thread string) {
	t.Helper()
	read := func() (string, string, sql.NullString) {
		var status, behavior string
		var action sql.NullString
		require.NoError(t, f.db.QueryRowContext(ctx, `SELECT i.status,i.behavior,i.resolved_action
		 FROM scheduled_task_runs r JOIN codex_turn_intents i ON i.id=r.intent_id
		 JOIN codex_thread_controls c ON c.id=i.control_id
		 WHERE c.external_thread_id=$1 AND r.trigger='run_now' ORDER BY r.created_at DESC LIMIT 1`, thread).
			Scan(&status, &behavior, &action))
		return status, behavior, action
	}
	status, behavior, action := read()
	require.Equal(t, "start_when_idle", behavior)
	require.Equal(t, "queued", status)
	require.False(t, action.Valid, "原工具回合未结束时不得决议调度输入")
	// 原生工具回合由模型响应栅栏保持活动，覆盖多个真实 Worker 领取周期。
	require.Never(t, func() bool {
		status, _, action := read()
		changed := status != "queued" || action.Valid
		if changed {
			t.Logf("原生模型响应仍被栅栏保持：调度状态=%s，决议=%s", status, action.String)
		}
		return changed
	}, time.Second, 50*time.Millisecond, "start_when_idle 必须等当前真实 Turn 结束，不能被偷换成 steer")
}

// 只输出调度身份和状态，不记录原始模型上下文、密钥或工具输入。
func logAutomationRetryState(t *testing.T, ctx context.Context, f controlRuntimeFixture, thread string) {
	t.Helper()
	for _, query := range []string{
		`SELECT row_to_json(s) FROM (SELECT i.id,i.status,i.behavior,i.resolved_action,i.confirmed_codex_turn_id,
		 i.available_at,i.attempt_count,i.last_error_code FROM codex_turn_intents i
		 JOIN codex_thread_controls c ON c.id=i.control_id WHERE c.external_thread_id=$1 ORDER BY i.created_at) s`,
		`SELECT row_to_json(s) FROM (SELECT r.id,r.status,r.trigger,r.intent_id,r.error_code FROM scheduled_task_runs r
		 JOIN codex_thread_controls c ON c.session_id=r.session_id WHERE c.external_thread_id=$1 ORDER BY r.created_at) s`,
	} {
		rows, err := f.db.QueryContext(ctx, query, thread)
		if err != nil {
			t.Logf("调度状态诊断失败：%v", err)
			continue
		}
		for rows.Next() {
			var state string
			if rows.Scan(&state) == nil {
				t.Logf("调度状态诊断：%s", state)
			}
		}
		_ = rows.Close()
	}
}
