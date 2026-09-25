//go:build integration

package discordintegration

import (
	"encoding/json"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/slovx2/tyrs-hand/internal/interactiveprotocol"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
)

func TestDiscordNativeApprovalStoresOnlyExplicitDecisions(t *testing.T) {
	db := discordDatabase(t)
	ctx := t.Context()
	require.NoError(t, database.Migrate(ctx, db))
	insertInteractiveGuild(t, db)
	seed := seedDiscordManagerData(t, db)
	manager := NewManager(db, nil)
	controlID, runID := insertInteractiveControlForEngine(t, db, seed, runtimeidentity.Claude)
	for _, method := range []string{interactiveprotocol.CommandApproval, interactiveprotocol.FileApproval} {
		params := json.RawMessage(`{"command":"printf approved","cwd":"/tmp/project","reason":"审批测试"}`)
		questions, err := interactiveprotocol.Questions(method, params)
		require.NoError(t, err)
		id := insertInteractiveRequest(t, db, controlID, runID, method, questions)
		_, err = db.ExecContext(ctx, `UPDATE codex_interactive_requests SET request_method=$2,request_params=$3 WHERE id=$1`, id, method, params)
		require.NoError(t, err)
		request, err := loadInteractiveProjection(ctx, db, id, false)
		require.NoError(t, err)
		card := interactiveCard(request)
		require.Contains(t, card.Header, "Claude Code")
		require.Contains(t, card.Header, "审批")
		require.Len(t, card.Buttons, 3)
		_, err = manager.AnswerInteractive(ctx, testGuildID, id, 0, -1, "允许本次")
		require.ErrorContains(t, err, "决策按钮")
		result, err := manager.AnswerInteractive(ctx, testGuildID, id, 0, 0, "")
		require.NoError(t, err)
		require.True(t, result.Complete)
		require.Contains(t, result.Card.Header, "Claude Code")
		require.Contains(t, result.Card.Sections[0], "允许本次")
		_, err = manager.AnswerInteractive(ctx, testGuildID, id, 0, 1, "")
		require.NoError(t, err, "迟到拒绝只返回已完成状态")
		var answer json.RawMessage
		require.NoError(t, db.QueryRowContext(ctx, `SELECT answer FROM codex_interactive_requests WHERE id=$1`, id).Scan(&answer))
		require.JSONEq(t, `{"decision":"accept"}`, string(answer))
	}
	for _, runStatus := range []string{"completed", "failed", "canceled", "running"} {
		t.Run("拒绝已结束任务_"+runStatus, func(t *testing.T) {
			params := json.RawMessage(`{"command":"printf late"}`)
			questions, err := interactiveprotocol.Questions(interactiveprotocol.CommandApproval, params)
			require.NoError(t, err)
			id := insertInteractiveRequest(t, db, controlID, runID, "late-"+runStatus, questions)
			_, err = db.ExecContext(ctx, `UPDATE codex_interactive_requests SET request_method=$2,request_params=$3 WHERE id=$1`,
				id, interactiveprotocol.CommandApproval, params)
			require.NoError(t, err)
			_, err = db.ExecContext(ctx, `UPDATE codex_turn_runs SET status=$2,active_slot=NULL,
				finished_at=CASE WHEN $2='running' THEN now() ELSE NULL END WHERE id=$1`, runID, runStatus)
			require.NoError(t, err)
			_, err = manager.AnswerInteractive(ctx, testGuildID, id, 0, 0, "")
			require.ErrorContains(t, err, "所属任务已结束")
			var unchanged bool
			require.NoError(t, db.QueryRowContext(ctx, `SELECT status='pending' AND answer IS NULL
				AND draft_answers='{}'::jsonb FROM codex_interactive_requests WHERE id=$1`, id).Scan(&unchanged))
			require.True(t, unchanged, "迟到的审批不能写入决策或草稿")
		})
	}
}
