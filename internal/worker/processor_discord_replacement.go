package worker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
)

var errReplacementSuperseded = errors.New("编辑更早的消息无法触发 Codex rollback；只有当前最新的已提交用户输入可以重跑")
var errDiscordTurnReplaced = errors.New("当前 Codex turn 已由消息编辑 replacement 取代")

func (p *Processor) prepareDiscordReplacement(ctx context.Context, runtime *codex.Runtime,
	claimed *codexcontrol.ClaimedControl, threadID string,
) error {
	if claimed.TargetIntentID == uuid.Nil {
		return errors.New("replacement 缺少 target_intent_id")
	}
	if err := p.assertReplacementStillLatest(ctx, claimed); err != nil {
		return err
	}
	var targetTurnID string
	if err := p.db.QueryRowContext(ctx, `SELECT COALESCE(confirmed_codex_turn_id,
		codex_submission_id, '') FROM codex_turn_intents WHERE id = $1`,
		claimed.TargetIntentID).Scan(&targetTurnID); err != nil {
		return err
	}
	if targetTurnID == "" {
		return errors.New("被替换输入尚未形成可 rollback 的 Codex turn")
	}

	snapshot, err := runtime.ReadThread(ctx, threadID)
	if err != nil {
		return err
	}
	target, exists := snapshot.TurnByID(targetTurnID)
	if exists {
		if len(snapshot.Turns) == 0 || snapshot.Turns[len(snapshot.Turns)-1].ID != targetTurnID {
			return errReplacementSuperseded
		}
		if target.Status == "inProgress" {
			if err := p.setReplacementPhase(ctx, claimed.ID, "interrupting", ""); err != nil {
				return err
			}
			if err := runtime.InterruptTurn(ctx, threadID, targetTurnID); err != nil {
				latest, readErr := runtime.ReadThread(ctx, threadID)
				if readErr != nil {
					return err
				}
				if current, ok := latest.TurnByID(targetTurnID); ok && current.Status == "inProgress" {
					return err
				}
			}
			if err := waitReplacementTurnTerminal(ctx, runtime, threadID, targetTurnID); err != nil {
				return err
			}
		}
		if err := p.setReplacementPhase(ctx, claimed.ID, "rollback_pending", ""); err != nil {
			return err
		}
		rollbackErr := runtime.RollbackThread(ctx, threadID, 1)
		latest, readErr := runtime.ReadThread(ctx, threadID)
		if readErr != nil {
			if rollbackErr != nil {
				return fmt.Errorf("rollback 响应未知且无法对账: %w", rollbackErr)
			}
			return readErr
		}
		if _, stillExists := latest.TurnByID(targetTurnID); stillExists {
			if rollbackErr != nil {
				return rollbackErr
			}
			return errors.New("thread/rollback 后目标 turn 仍然存在")
		}
	}
	if err := p.assertReplacementStillLatest(ctx, claimed); err != nil {
		return err
	}
	if err := p.setReplacementPhase(ctx, claimed.ID, "rollback_applied", ""); err != nil {
		return err
	}
	return p.setReplacementPhase(ctx, claimed.ID, "start_pending", "")
}

func waitReplacementTurnTerminal(ctx context.Context, runtime *codex.Runtime, threadID,
	turnID string,
) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return errors.New("等待被中断 turn 进入终态超时")
		case <-ticker.C:
			snapshot, err := runtime.ReadThread(ctx, threadID)
			if err != nil {
				continue
			}
			turn, ok := snapshot.TurnByID(turnID)
			if !ok || turn.Status != "inProgress" {
				return nil
			}
		}
	}
}

func (p *Processor) assertReplacementStillLatest(ctx context.Context,
	claimed *codexcontrol.ClaimedControl,
) error {
	var latestID string
	err := p.db.QueryRowContext(ctx, `SELECT id::text FROM codex_turn_intents
		WHERE control_id = $1 AND operation IN ('turn_input','replace_last_turn')
		ORDER BY sequence_no DESC LIMIT 1`, claimed.ControlID).Scan(&latestID)
	if errors.Is(err, sql.ErrNoRows) || latestID != claimed.ID.String() {
		return errReplacementSuperseded
	}
	return err
}

func (p *Processor) setReplacementPhase(ctx context.Context, intentID uuid.UUID, phase,
	message string,
) error {
	_, err := p.db.ExecContext(ctx, `UPDATE codex_turn_intents SET replacement_phase = $2,
		replacement_error = NULLIF($3,''),
		resolved_action = CASE WHEN $2 = 'running' THEN 'replace' ELSE resolved_action END,
		updated_at = now() WHERE id = $1`, intentID, phase, message)
	return err
}

func (p *Processor) finalizeReplacementMessages(ctx context.Context,
	claimed *codexcontrol.ClaimedControl,
) error {
	_, err := p.db.ExecContext(ctx, `UPDATE discord_input_messages
		SET replacement_previous_intent_id = NULL WHERE turn_intent_id = $1`, claimed.ID)
	return err
}

func (p *Processor) releaseTerminalReplacement(ctx context.Context,
	claimed *codexcontrol.ClaimedControl, cause error,
) {
	if claimed.Attempt < claimed.MaxAttempts || claimed.SubmissionID != "" {
		return
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `UPDATE discord_input_messages
		SET turn_intent_id = replacement_previous_intent_id,
			replacement_previous_intent_id = NULL WHERE turn_intent_id = $1`, claimed.ID)
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE codex_turn_intents SET replacement_phase = 'terminal',
			replacement_error = $2, updated_at = now() WHERE id = $1`, claimed.ID, cause.Error())
	}
	if err == nil {
		_ = tx.Commit()
	}
}
