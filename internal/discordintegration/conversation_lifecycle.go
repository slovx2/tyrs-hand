package discordintegration

import (
	"context"
	"database/sql"
	"errors"
	"strconv"

	"github.com/google/uuid"
)

func lifecycleCard(conversationID uuid.UUID, revision int64,
) ComponentCardPayload {
	return ComponentCardPayload{AccentColor: cardColorGray,
		Header: "🔒 Codex · 会话已归档",
		Body:   "该会话已关闭，历史消息仍然保留。",
		Buttons: []ComponentButtonPayload{{
			Label:    "恢复会话",
			CustomID: "codex-restore:" + conversationID.String() + ":" + int64Text(revision),
			Style:    "primary",
		}},
	}
}

func int64Text(value int64) string {
	return strconv.FormatInt(value, 10)
}

// EnqueueConversationLifecycleTx 将 app-server 生命周期投影到原 Discord Post。
// archived 先投递恢复卡片；active 先解锁 Post，再删除原归档卡片。
func EnqueueConversationLifecycleTx(ctx context.Context, tx *sql.Tx,
	conversationID uuid.UUID,
) error {
	var threadID, state, cardMessageID string
	var revision int64
	err := tx.QueryRowContext(ctx, `SELECT thread_id, lifecycle_state,
		lifecycle_revision,
		COALESCE(lifecycle_card_message_id,'')
		FROM discord_conversations WHERE id = $1 FOR UPDATE`,
		conversationID).Scan(&threadID, &state, &revision,
		&cardMessageID)
	if err != nil {
		return err
	}
	if state != "active" && state != "archived" {
		return nil
	}
	if state == "archived" {
		ready, err := conversationLifecycleProjectionReady(ctx, tx, conversationID)
		if err != nil || !ready {
			return err
		}
	}
	if state == "active" {
		return enqueueThreadLifecycle(ctx, tx, conversationID, threadID, state, revision)
	}
	card := lifecycleCard(conversationID, revision)
	cardKey := "conversation-lifecycle-card:" + conversationID.String()
	cardPayload := map[string]any{
		"channelId": threadID, "card": card, "conversationId": conversationID.String(),
		"lifecycleState": state, "revision": revision,
	}
	cardOperation, cardNonce := "message.create", cardKey
	if cardMessageID != "" {
		cardOperation, cardNonce = "message.update", ""
		cardPayload["messageId"] = cardMessageID
	}
	if err := enqueueDiscordOutbox(ctx, tx, cardKey, cardOperation,
		"channels/"+threadID+"/messages", cardPayload, cardNonce); err != nil {
		return err
	}
	return nil
}

func conversationLifecycleProjectionReady(ctx context.Context, tx *sql.Tx,
	conversationID uuid.UUID,
) (bool, error) {
	var active bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM codex_turn_runs run
		JOIN codex_thread_controls control ON control.id = run.control_id
		JOIN discord_conversations conversation ON conversation.id = control.discord_conversation_id
		WHERE conversation.id = $1
			AND run.status IN ('starting','running','waiting_for_user','reconciling')
	)`, conversationID).Scan(&active)
	if err != nil || active {
		return false, err
	}
	var pending bool
	prefix := conversationID.String()
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM integration_outbox
		WHERE integration = 'discord' AND status <> 'completed' AND (
			operation_key LIKE $1 OR operation_key LIKE $2 OR operation_key LIKE $3
		)
	)`, "projection:conversation:"+prefix+":%",
		"conversation-reply:"+prefix+":%", "desktop-input:"+prefix+":%").Scan(&pending)
	return !pending, err
}

func enqueueThreadLifecycle(ctx context.Context, execer discordOutboxExecer,
	conversationID uuid.UUID, threadID, state string, revision int64,
) error {
	archived := state == "archived"
	return enqueueDiscordOutbox(ctx, execer,
		"conversation-lifecycle:"+conversationID.String(), "thread.lifecycle",
		"channels/"+threadID, map[string]any{
			"channelId": threadID, "conversationId": conversationID.String(),
			"lifecycleState": state, "revision": revision,
			"archived": archived, "locked": archived,
		}, "")
}

func ReconcileConversationLifecycles(ctx context.Context, db *sql.DB,
	guildID string,
) error {
	rows, err := db.QueryContext(ctx, `SELECT id FROM discord_conversations
		WHERE guild_id = $1 AND lifecycle_state IN ('active','archived')
			AND (lifecycle_revision > discord_lifecycle_applied_revision
				OR (lifecycle_state = 'active' AND lifecycle_card_message_id IS NOT NULL))
		ORDER BY updated_at, id LIMIT 100`, guildID)
	if err != nil {
		return err
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	var failures []error
	for _, id := range ids {
		tx, err := db.BeginTx(ctx, nil)
		if err == nil {
			err = EnqueueConversationLifecycleTx(ctx, tx, id)
		}
		if err == nil {
			err = tx.Commit()
		} else if tx != nil {
			_ = tx.Rollback()
		}
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
