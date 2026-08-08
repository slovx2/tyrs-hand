package discordintegration

import (
	"context"
	"database/sql"

	"github.com/google/uuid"
)

func conversationHasActionablePlanTx(ctx context.Context, tx *sql.Tx,
	conversationID uuid.UUID,
) (bool, error) {
	var active bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM official_plan_actions action
		WHERE action.conversation_id=$1 AND action.status='available'
		AND NOT EXISTS(SELECT 1 FROM official_turn_submissions submission
			WHERE submission.conversation_id=$1
			  AND submission.created_at>action.created_at)
	)`, conversationID).Scan(&active)
	return active, err
}
