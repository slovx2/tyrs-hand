package discordintegration

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

// Archive 登记 Discord 发起的真实 Codex 归档；活动 Turn 会自然结束后再交给 Worker。
func (s *ConversationService) Archive(ctx context.Context, guildID, threadID,
	requesterID string,
) (workerprotocol.ThreadLifecycleState, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return workerprotocol.ThreadLifecycleState{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var result workerprotocol.ThreadLifecycleState
	var conversationID, forumID uuid.UUID
	var ownerID, currentState string
	err = tx.QueryRowContext(ctx, `SELECT conversation.id, conversation.forum_id,
		conversation.owner_discord_user_id, conversation.lifecycle_state,
		conversation.lifecycle_revision, control.id,
		control.development_environment_id, control.external_thread_id
		FROM discord_conversations conversation
		JOIN codex_thread_controls control
			ON control.discord_conversation_id = conversation.id
		WHERE conversation.guild_id = $1 AND conversation.thread_id = $2
		ORDER BY control.created_at LIMIT 1
		FOR UPDATE OF conversation, control`, guildID, threadID).
		Scan(&conversationID, &forumID, &ownerID, &currentState, &result.Revision,
			&result.ControlID, &result.EnvironmentID, &result.ThreadID)
	if err != nil {
		return workerprotocol.ThreadLifecycleState{}, err
	}
	if _, err := s.access(ctx, tx, forumID, ownerID, requesterID); err != nil {
		return workerprotocol.ThreadLifecycleState{}, err
	}
	result.DesiredState = "archived"
	if currentState == "archived" {
		result.Status = "completed"
		return result, tx.Commit()
	}
	if currentState == "archive_pending" {
		err = tx.QueryRowContext(ctx, `SELECT id, status
			FROM codex_thread_lifecycle_requests
			WHERE control_id = $1 AND desired_state = 'archived'
				AND status IN ('waiting_for_turn','applying')
			ORDER BY created_at DESC LIMIT 1`, result.ControlID).
			Scan(&result.ID, &result.Status)
		if err == nil {
			return result, tx.Commit()
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return workerprotocol.ThreadLifecycleState{}, err
		}
	}
	var active bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM codex_turn_runs WHERE control_id = $1
			AND status IN ('starting','running','waiting_for_user','reconciling')
	)`, result.ControlID).Scan(&active); err != nil {
		return workerprotocol.ThreadLifecycleState{}, err
	}
	result.ID, result.Status = uuid.New(), "applying"
	if active {
		result.Status = "waiting_for_turn"
	}
	result.Revision++
	_, err = tx.ExecContext(ctx, `UPDATE codex_thread_lifecycle_requests SET
		status = 'canceled', completed_at = now(), updated_at = now()
		WHERE control_id = $1 AND status IN ('waiting_for_turn','applying')`,
		result.ControlID)
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE codex_thread_controls SET
			lifecycle_state = 'archive_pending', lifecycle_revision = $2,
			lifecycle_last_error = NULL, updated_at = now() WHERE id = $1`,
			result.ControlID, result.Revision)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE discord_conversations SET
			lifecycle_state = 'archive_pending', lifecycle_revision = $2,
			lifecycle_projection_error = NULL, updated_at = now() WHERE id = $1`,
			conversationID, result.Revision)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO codex_thread_lifecycle_requests
			(id, control_id, environment_id, source, desired_state, status, revision,
				requested_by_discord_user_id)
			VALUES ($1,$2,$3,'discord','archived',$4,$5,$6)`,
			result.ID, result.ControlID, result.EnvironmentID, result.Status,
			result.Revision, requesterID)
	}
	if err != nil {
		return workerprotocol.ThreadLifecycleState{}, err
	}
	return result, tx.Commit()
}
