package codexcontrol

import (
	"context"
	"database/sql"
	"errors"
)

var ErrSubmissionConflict = errors.New("提交或 Turn 标识与已登记记录冲突")

// 与终态写入保持 intent -> run 的加锁顺序，迟到确认不得把终态重新变成 running。
func lockSubmissionState(ctx context.Context, tx *sql.Tx, claimed *ClaimedControl, value string, confirming bool) (bool, error) {
	var intentStatus, runStatus string
	var submission, confirmed sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT status,codex_submission_id,confirmed_codex_turn_id
		FROM codex_turn_intents WHERE id=$1 FOR UPDATE`, claimed.ID).Scan(&intentStatus, &submission, &confirmed); err != nil {
		return false, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT status FROM codex_turn_runs WHERE id=$1 FOR UPDATE`, claimed.RunID).Scan(&runStatus); err != nil {
		return false, err
	}
	stored := submission
	if confirming {
		stored = confirmed
	}
	if value == "" || (stored.Valid && stored.String != value) {
		return false, ErrSubmissionConflict
	}
	terminal := func(status string) bool { return status == "completed" || status == "failed" || status == "canceled" }
	if terminal(intentStatus) || terminal(runStatus) {
		return true, nil
	}
	// 确认之后重发同一 submission 只做对账，不能降级到 awaiting_confirmation。
	return !confirming && confirmed.Valid && submission.Valid, nil
}
