package discordintegration

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/officialapp"
)

var ErrLifecycleRevisionStale = errors.New("这张恢复卡片已经过期，请使用最新卡片")

type ThreadActionState struct {
	ID             uuid.UUID
	Status         string
	DesiredState   string
	Revision       int64
	AlreadyInState bool
}

func (s *ConversationService) Archive(ctx context.Context, guildID, threadID,
	requesterID, sourceOrder string,
) (ThreadActionState, error) {
	return s.enqueueLifecycleAction(ctx, guildID, threadID, requesterID, sourceOrder,
		nil, "archive")
}

func (s *ConversationService) Restore(ctx context.Context, guildID, threadID,
	requesterID, sourceOrder string, expectedRevision *int64,
) (ThreadActionState, error) {
	return s.enqueueLifecycleAction(ctx, guildID, threadID, requesterID, sourceOrder,
		expectedRevision, "unarchive")
}

func (s *ConversationService) enqueueLifecycleAction(ctx context.Context, guildID,
	threadID, requesterID, sourceOrder string, expectedRevision *int64, action string,
) (ThreadActionState, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ThreadActionState{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var result ThreadActionState
	var conversationID, forumID, workspaceID uuid.UUID
	var ownerID, currentState string
	err = tx.QueryRowContext(ctx, `SELECT conversation.id,conversation.forum_id,
		conversation.owner_discord_user_id,conversation.lifecycle_state,
		conversation.lifecycle_revision,binding.workspace_id
		FROM discord_conversations conversation
		JOIN official_thread_bindings binding ON binding.conversation_id=conversation.id
		WHERE conversation.guild_id=$1 AND conversation.thread_id=$2
		FOR UPDATE OF conversation,binding`, guildID, threadID).Scan(&conversationID,
		&forumID, &ownerID, &currentState, &result.Revision, &workspaceID)
	if err != nil {
		return ThreadActionState{}, err
	}
	if _, err = s.access(ctx, tx, forumID, ownerID, requesterID); err != nil {
		return ThreadActionState{}, err
	}
	if expectedRevision != nil && result.Revision != *expectedRevision {
		return ThreadActionState{}, ErrLifecycleRevisionStale
	}
	desired, pending := "archived", "archive_pending"
	if action == "unarchive" {
		desired, pending = "active", "unarchive_pending"
	}
	result.DesiredState = desired
	if currentState == desired {
		result.Status, result.AlreadyInState = "completed", true
		return result, tx.Commit()
	}
	if (action == "archive" && currentState == "archive_pending") ||
		(action == "unarchive" && currentState == "unarchive_pending") {
		err = tx.QueryRowContext(ctx, `SELECT id,status FROM official_thread_actions
			WHERE conversation_id=$1 AND action=$2 AND status IN ('queued','applying')
			ORDER BY created_at DESC,id DESC LIMIT 1`, conversationID, action).
			Scan(&result.ID, &result.Status)
		if err == nil {
			return result, tx.Commit()
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return ThreadActionState{}, err
		}
	}
	idempotencyKey := "discord:" + action + ":" + sourceOrder
	result.ID, _, err = officialapp.EnqueueThreadActionTx(ctx, tx, workspaceID,
		conversationID, sourceOrder, idempotencyKey, action)
	if err != nil {
		return ThreadActionState{}, err
	}
	result.Status = "queued"
	if currentState != pending {
		result.Revision++
	}
	_, err = tx.ExecContext(ctx, `UPDATE discord_conversations SET lifecycle_state=$2,
		lifecycle_revision=$3,updated_at=now() WHERE id=$1`, conversationID, pending,
		result.Revision)
	if err != nil {
		return ThreadActionState{}, err
	}
	if err = tx.Commit(); err != nil {
		return ThreadActionState{}, err
	}
	s.notifyJobs(ctx)
	return result, nil
}
