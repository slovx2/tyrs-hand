package discordintegration

import (
	"context"
	"database/sql"

	"github.com/google/uuid"
)

type OfficialThreadSettings struct {
	Model             string
	ReasoningEffort   string
	ServiceTier       string
	CollaborationMode string
}

func ApplyOfficialThreadSettings(ctx context.Context, db *sql.DB, workspaceID uuid.UUID,
	threadID string, settings OfficialThreadSettings,
) error {
	mode := settings.CollaborationMode
	if mode != "default" && mode != "plan" {
		mode = ""
	}
	result, err := db.ExecContext(ctx, `UPDATE discord_conversations conversation SET
		model=NULLIF($3,''),reasoning_effort=NULLIF($4,''),service_tier=NULLIF($5,''),
		collaboration_mode=CASE WHEN $6='' THEN conversation.collaboration_mode ELSE $6 END,
		collaboration_mode_revision=conversation.collaboration_mode_revision+CASE
			WHEN $6<>'' AND conversation.collaboration_mode<>$6 THEN 1 ELSE 0 END,
		settings_revision=conversation.settings_revision+CASE WHEN
			conversation.model IS DISTINCT FROM NULLIF($3,'') OR
			conversation.reasoning_effort IS DISTINCT FROM NULLIF($4,'') OR
			conversation.service_tier IS DISTINCT FROM NULLIF($5,'') OR
			($6<>'' AND conversation.collaboration_mode<>$6) THEN 1 ELSE 0 END,
		configuration_status='configured',updated_at=now()
		FROM official_thread_bindings binding
		WHERE binding.conversation_id=conversation.id AND binding.workspace_id=$1
		AND binding.thread_id=$2`, workspaceID, threadID, settings.Model,
		settings.ReasoningEffort, settings.ServiceTier, mode)
	if err != nil {
		return err
	}
	_, err = result.RowsAffected()
	return err
}

// EnqueueOfficialThreadNameTx 将 App Server 的权威标题写回绑定会话和 Discord Post。
func EnqueueOfficialThreadNameTx(ctx context.Context, tx *sql.Tx,
	conversationID uuid.UUID, threadName string,
) error {
	title := normalizeConversationTitle(threadName)
	if title == "" {
		return nil
	}
	var threadID string
	err := tx.QueryRowContext(ctx, `UPDATE discord_conversations SET
		title=$2,generated_title=$2,title_rename_status='scheduled',updated_at=now()
		WHERE id=$1 RETURNING thread_id`, conversationID, title).Scan(&threadID)
	if err != nil {
		return err
	}
	payload := map[string]any{"channelId": threadID, "threadName": title,
		"conversationId": conversationID.String()}
	return enqueueDiscordOutbox(ctx, tx, "conversation-title:"+conversationID.String(),
		"thread.rename", "channels/"+threadID, payload, "")
}
