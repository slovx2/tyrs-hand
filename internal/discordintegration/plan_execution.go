package discordintegration

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
)

const (
	planExecuteButtonPrefix = "codex-plan-execute:"
)

var (
	ErrPlanExecutionBusy  = errors.New("当前会话仍在处理中，请等待本轮结束后再执行计划")
	ErrPlanExecutionStale = errors.New("这个执行按钮对应的已不是当前最新 Plan")
)

type PlanExecutionResult struct {
	AlreadyExecuted bool
}

func (s *ConversationService) ExecutePlan(ctx context.Context, guildID, threadID, userID,
	displayName, username string, runID uuid.UUID,
) (PlanExecutionResult, error) {
	if runID == uuid.Nil {
		return PlanExecutionResult{}, errors.New("计划 Run ID 无效")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PlanExecutionResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var controlID, conversationID, forumID uuid.UUID
	var runStatus, runMode, actualGuildID, actualThreadID, ownerID, planContent string
	var planOutputType string
	var lifecycle, conversationStatus, currentMode string
	var runStarted time.Time
	var runFinished sql.NullTime
	var settingsRevision int64
	err = tx.QueryRowContext(ctx, `SELECT run.control_id, conversation.id,
		conversation.forum_id, run.status, run.collaboration_mode,
		conversation.guild_id, conversation.thread_id,
		conversation.owner_discord_user_id, conversation.lifecycle_state,
		conversation.status, session.collaboration_mode,session.settings_version,
		run.started_at, run.finished_at,
		COALESCE(plan_intent.result->>'finalAnswer',''),
		COALESCE(plan_intent.result->>'finalOutputType','')
		FROM codex_turn_runs run
		JOIN codex_thread_controls control ON control.id = run.control_id
		JOIN codex_turn_intents plan_intent ON plan_intent.id=run.primary_intent_id
		JOIN discord_conversations conversation
			ON conversation.id = control.discord_conversation_id
		JOIN workspace_sessions session ON session.id=control.session_id
		WHERE run.id = $1 FOR UPDATE OF run, control, session, conversation`, runID).Scan(
		&controlID, &conversationID, &forumID, &runStatus, &runMode,
		&actualGuildID, &actualThreadID, &ownerID, &lifecycle, &conversationStatus,
		&currentMode, &settingsRevision, &runStarted, &runFinished,
		&planContent, &planOutputType)
	if errors.Is(err, sql.ErrNoRows) {
		return PlanExecutionResult{}, errors.New("这个 Plan 已不存在")
	}
	if err != nil {
		return PlanExecutionResult{}, err
	}
	if actualGuildID != guildID || actualThreadID != threadID {
		return PlanExecutionResult{}, errors.New("这个执行按钮不属于当前 Codex 会话")
	}
	access, err := s.access(ctx, tx, forumID, ownerID, userID)
	if err != nil {
		if errors.Is(err, ErrReadOnly) {
			return PlanExecutionResult{}, errors.New("readonly 用户不能执行计划")
		}
		return PlanExecutionResult{}, err
	}
	messageID := "plan-execution:" + runID.String()
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM discord_input_messages WHERE message_id = $1
	)`, messageID).Scan(&exists); err != nil {
		return PlanExecutionResult{}, err
	}
	if exists {
		return PlanExecutionResult{AlreadyExecuted: true}, tx.Commit()
	}
	if runStatus != "completed" || runMode != "plan" || !runFinished.Valid ||
		planContent == "" || planOutputType != "plan" {
		return PlanExecutionResult{}, errors.New("这个 Plan 尚未完成，不能执行")
	}
	if lifecycle != "active" || conversationStatus != "active" {
		return PlanExecutionResult{}, codexcontrol.ErrControlArchived
	}
	var busy bool
	err = tx.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM codex_turn_intents intent
			WHERE intent.control_id = $1 AND intent.status IN
			('placement_pending','queued','dispatching','awaiting_confirmation',
			 'running','waiting_for_user','reconciling','retry_wait')) OR
		EXISTS(SELECT 1 FROM codex_turn_runs active
			WHERE active.control_id = $1 AND active.status IN
			('starting','running','waiting_for_user','reconciling'))`, controlID).Scan(&busy)
	if err != nil {
		return PlanExecutionResult{}, err
	}
	if busy {
		return PlanExecutionResult{}, ErrPlanExecutionBusy
	}
	var stale bool
	err = tx.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM codex_turn_runs newer
			WHERE newer.control_id = $1 AND newer.id <> $2
			AND newer.started_at > $3) OR
		EXISTS(SELECT 1 FROM codex_turn_intents newer
			WHERE newer.control_id = $1
			AND newer.id <> (SELECT primary_intent_id FROM codex_turn_runs WHERE id=$2)
			AND newer.created_at > $4)`,
		controlID, runID, runStarted, runFinished.Time).Scan(&stale)
	if err != nil {
		return PlanExecutionResult{}, err
	}
	if stale {
		return PlanExecutionResult{}, ErrPlanExecutionStale
	}
	if currentMode != "default" {
		settingsRevision++
		_, err = tx.ExecContext(ctx, `UPDATE workspace_sessions SET
			collaboration_mode='default',settings_version=$2,updated_at=now() WHERE id=(
				SELECT session_id FROM codex_thread_controls WHERE id=$1)`, controlID,
			settingsRevision)
		if err != nil {
			return PlanExecutionResult{}, err
		}
	}
	var sessionID uuid.UUID
	if err = tx.QueryRowContext(ctx, `SELECT session_id FROM codex_thread_controls WHERE id=$1`,
		controlID).Scan(&sessionID); err != nil {
		return PlanExecutionResult{}, err
	}
	if err = codexcontrol.ProjectWorkspaceSessionSettingsTx(ctx, tx, sessionID); err != nil {
		return PlanExecutionResult{}, err
	}
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		displayName = strings.TrimSpace(username)
	}
	inserted, err := s.insertMessage(ctx, tx, conversationID, access, IncomingMessage{
		GuildID: guildID, ThreadID: threadID, MessageID: messageID,
		DiscordUserID: userID, DisplayName: displayName, Username: username,
		Body: codexcontrol.PlanExecutionInstruction(planContent),
	})
	if err != nil {
		return PlanExecutionResult{}, err
	}
	if !inserted {
		return PlanExecutionResult{AlreadyExecuted: true}, tx.Commit()
	}
	if err := s.enqueueMessageWithDisplay(ctx, tx, conversationID, messageID,
		codexcontrol.PlanExecutionDisplayText); err != nil {
		return PlanExecutionResult{}, err
	}
	if err := ProjectConversationThinkingTx(ctx, tx, guildID, threadID,
		conversationID, messageID); err != nil {
		return PlanExecutionResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return PlanExecutionResult{}, err
	}
	s.notifyJobs(ctx)
	return PlanExecutionResult{}, nil
}

func planExecutionStartedCard() ComponentCardPayload {
	return ComponentCardPayload{AccentColor: cardColorGreen,
		Header: "✅ " + codexcontrol.PlanExecutionDisplayText, Body: "`模式：Default`"}
}
