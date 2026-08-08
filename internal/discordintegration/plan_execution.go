package discordintegration

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/officialapp"
)

const planExecuteButtonPrefix = "codex-plan-execute:"

var ErrPlanExecutionStale = errors.New("这个执行按钮对应的已不是当前最新 Plan")

type PlanExecutionResult struct {
	AlreadyExecuted bool
}

func (s *ConversationService) ExecutePlan(ctx context.Context, guildID, threadID, userID,
	displayName, username string, actionID uuid.UUID, sourceOrder string,
) (PlanExecutionResult, error) {
	if actionID == uuid.Nil {
		return PlanExecutionResult{}, errors.New("计划 action ID 无效")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PlanExecutionResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var workspaceID, conversationID, forumID uuid.UUID
	var status, actualGuildID, actualThreadID, ownerID, lifecycle, conversationStatus string
	var planText string
	err = tx.QueryRowContext(ctx, `SELECT action.workspace_id,action.conversation_id,
		conversation.forum_id,action.status,conversation.guild_id,conversation.thread_id,
		conversation.owner_discord_user_id,conversation.lifecycle_state,conversation.status,
		action.plan_text FROM official_plan_actions action
		JOIN discord_conversations conversation ON conversation.id=action.conversation_id
		WHERE action.id=$1 FOR UPDATE OF action,conversation`, actionID).Scan(&workspaceID,
		&conversationID, &forumID, &status, &actualGuildID, &actualThreadID, &ownerID,
		&lifecycle, &conversationStatus, &planText)
	if errors.Is(err, sql.ErrNoRows) {
		return PlanExecutionResult{}, errors.New("这个 Plan 已不存在")
	}
	if err != nil {
		return PlanExecutionResult{}, err
	}
	if actualGuildID != guildID || actualThreadID != threadID {
		return PlanExecutionResult{}, errors.New("这个执行按钮不属于当前 Codex 会话")
	}
	if _, err = s.access(ctx, tx, forumID, ownerID, userID); err != nil {
		if errors.Is(err, ErrReadOnly) {
			return PlanExecutionResult{}, errors.New("readonly 用户不能执行计划")
		}
		return PlanExecutionResult{}, err
	}
	if status == "executed" {
		return PlanExecutionResult{AlreadyExecuted: true}, tx.Commit()
	}
	if status != "available" {
		return PlanExecutionResult{}, ErrPlanExecutionStale
	}
	var newer bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM official_plan_actions
		WHERE conversation_id=$1 AND status='available' AND created_at>(
			SELECT created_at FROM official_plan_actions WHERE id=$2))`, conversationID,
		actionID).Scan(&newer); err != nil {
		return PlanExecutionResult{}, err
	}
	if newer {
		return PlanExecutionResult{}, ErrPlanExecutionStale
	}
	if lifecycle != "active" || conversationStatus != "active" {
		return PlanExecutionResult{}, codexcontrol.ErrControlArchived
	}
	_, err = tx.ExecContext(ctx, `UPDATE discord_conversations SET collaboration_mode='default',
		collaboration_mode_revision=collaboration_mode_revision+1,
		settings_revision=settings_revision+1,updated_at=now() WHERE id=$1`, conversationID)
	if err != nil {
		return PlanExecutionResult{}, err
	}
	instruction := codexcontrol.PlanExecutionInstruction(planText)
	_, inserted, err := officialapp.EnqueueTx(ctx, tx, officialapp.EnqueueRequest{
		WorkspaceID: workspaceID, ConversationID: conversationID, PlanActionID: actionID,
		SourceType: "discord_plan", SourceOrder: sourceOrder,
		ClientMessageID: "discord-plan:" + actionID.String(), Instruction: instruction,
		DisplayInstruction: codexcontrol.PlanExecutionDisplayText,
		Input:              []officialapp.UserInput{officialapp.TextInput(instruction)},
		Preferences:        officialapp.Preferences{CollaborationMode: "default"},
	})
	if err != nil {
		return PlanExecutionResult{}, err
	}
	if !inserted {
		return PlanExecutionResult{AlreadyExecuted: true}, tx.Commit()
	}
	_, err = tx.ExecContext(ctx, `UPDATE official_plan_actions SET status='executed',
		executed_at=now() WHERE id=$1 AND status='available'`, actionID)
	if err != nil {
		return PlanExecutionResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return PlanExecutionResult{}, err
	}
	s.notifyJobs(ctx)
	return PlanExecutionResult{}, nil
}

func planExecutionStartedCard() ComponentCardPayload {
	return ComponentCardPayload{AccentColor: cardColorGreen,
		Header: "✅ " + codexcontrol.PlanExecutionDisplayText, Body: "`模式：Default`"}
}
