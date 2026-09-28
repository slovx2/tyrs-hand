package httpapi

import (
	"context"
	"database/sql"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
)

func lockWorkerRunParents(ctx context.Context, tx *sql.Tx, claimed *codexcontrol.ClaimedControl) error {
	var exists bool
	// Session 从实际 Run 关联读取；GitHub Run 没有 Session，也须继续锁 Control。
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM codex_turn_runs run
		JOIN codex_thread_controls control ON control.id=run.control_id
		JOIN workspace_sessions session ON session.id=control.session_id
		WHERE run.id=$1 AND run.control_id=$2 AND run.primary_intent_id=$3
		FOR NO KEY UPDATE OF session)`, claimed.RunID, claimed.ControlID, claimed.ID).Scan(&exists); err != nil {
		return err
	}
	var locked uuid.UUID
	if err := tx.QueryRowContext(ctx, `SELECT id FROM codex_thread_controls
		WHERE id=$1 FOR NO KEY UPDATE`, claimed.ControlID).Scan(&locked); err != nil {
		return err
	}
	return tx.QueryRowContext(ctx, `SELECT id FROM codex_turn_intents
		WHERE id=$1 AND control_id=$2 FOR NO KEY UPDATE`, claimed.ID, claimed.ControlID).Scan(&locked)
}
