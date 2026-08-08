package discordintegration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/officialapp"
)

type discordPostResult struct {
	ThreadID  string `json:"threadId"`
	MessageID string `json:"messageId"`
}

func (s *SQLoutbox) completeOfficialThreadPost(ctx context.Context, tx *sql.Tx,
	item OutboxItem, response json.RawMessage,
) (uuid.UUID, error) {
	bindingID, err := uuid.Parse(strings.TrimPrefix(item.OperationKey,
		"official-thread-post:"))
	if err != nil {
		return uuid.Nil, errors.New("官方 Thread Post operation key 无效")
	}
	var result discordPostResult
	if json.Unmarshal(response, &result) != nil || result.ThreadID == "" ||
		result.MessageID == "" {
		return uuid.Nil, errors.New("官方 Thread Post Outbox 结果无效")
	}
	var payload struct {
		ThreadName string `json:"threadName"`
	}
	_ = json.Unmarshal(item.Payload, &payload)
	var existingConversation uuid.NullUUID
	var projectID, forumID, profileID uuid.UUID
	var lifecycle, guildID, ownerID, projectName string
	err = tx.QueryRowContext(ctx, `SELECT binding.conversation_id,
		binding.workspace_project_id,binding.lifecycle_state,
		forum.id,forum.guild_id,COALESCE(forum.owner_discord_user_id,
			workspace.owner_discord_user_id),project.name,profile.id
		FROM official_thread_bindings binding
		JOIN worker_workspaces workspace ON workspace.id=binding.workspace_id
		JOIN workspace_projects project ON project.id=binding.workspace_project_id
		JOIN discord_forums forum ON forum.workspace_project_id=project.id
			AND forum.workspace_id=workspace.id AND forum.binding_status='active'
		JOIN LATERAL (SELECT id FROM agent_profiles ORDER BY created_at,id LIMIT 1)
			profile ON true
		WHERE binding.id=$1 FOR UPDATE OF binding`, bindingID).
		Scan(&existingConversation, &projectID, &lifecycle,
			&forumID, &guildID, &ownerID, &projectName, &profileID)
	if err != nil {
		return uuid.Nil, err
	}
	if existingConversation.Valid {
		return bindingID, nil
	}
	title := strings.TrimSpace(payload.ThreadName)
	if title == "" {
		title = projectName + " · Desktop"
	}
	conversationID := uuid.New()
	_, err = tx.ExecContext(ctx, `INSERT INTO discord_conversations(
		id,guild_id,forum_id,thread_id,starter_message_id,owner_discord_user_id,
		workspace_project_id,agent_profile_id,title,status,configuration_status,
		configured_by_discord_user_id,title_rename_status,lifecycle_state)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'active','configured',$6,'skipped',$10)`,
		conversationID, guildID, forumID, result.ThreadID, result.MessageID, ownerID,
		projectID, profileID, title, lifecycle)
	if err != nil {
		return uuid.Nil, err
	}
	resultUpdate, err := tx.ExecContext(ctx, `UPDATE official_thread_bindings SET
		conversation_id=$2,updated_at=now() WHERE id=$1 AND conversation_id IS NULL`,
		bindingID, conversationID)
	if err != nil {
		return uuid.Nil, err
	}
	if count, _ := resultUpdate.RowsAffected(); count != 1 {
		return uuid.Nil, errors.New("官方 Thread Discord 绑定已被并发修改")
	}
	return bindingID, nil
}

func ReplayOfficialThreadProjection(ctx context.Context, db *sql.DB,
	bindingID uuid.UUID,
) error {
	var workspaceID uuid.UUID
	var raw json.RawMessage
	err := db.QueryRowContext(ctx, `SELECT binding.workspace_id,projection.thread
		FROM official_thread_bindings binding
		JOIN official_thread_projections projection
			ON projection.workspace_id=binding.workspace_id
			AND projection.thread_id=binding.thread_id
		WHERE binding.id=$1`, bindingID).Scan(&workspaceID, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var thread officialapp.Thread
	if err = json.Unmarshal(raw, &thread); err != nil {
		return err
	}
	return ProjectOfficialThread(ctx, db, workspaceID, thread)
}
