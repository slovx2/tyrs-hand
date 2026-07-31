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
	err := tx.QueryRowContext(ctx, `SELECT COALESCE((
		SELECT intent.status = 'completed'
			AND COALESCE(intent.result->>'finalOutputType','') = 'plan'
		FROM codex_turn_intents intent
		WHERE intent.discord_conversation_id = $1
			AND intent.operation IN ('turn_input','replace_last_turn')
		ORDER BY intent.sequence_no DESC, intent.created_at DESC, intent.id DESC LIMIT 1
	), false)`, conversationID).Scan(&active)
	return active, err
}

// ExpireConversationPlanCards 在下一轮 Codex turn 已实际开始后删除旧 Plan 操作卡。
func ExpireConversationPlanCards(ctx context.Context, db *sql.DB, conversationID,
	startedRunID uuid.UUID,
) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var guildID, threadID string
	if err := tx.QueryRowContext(ctx, `SELECT guild_id, thread_id FROM discord_conversations
		WHERE id=$1`, conversationID).Scan(&guildID, &threadID); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT projection_key, COALESCE(message_id,'')
		FROM discord_projections WHERE guild_id=$1 AND resource_id=$2
			AND projection_key LIKE $3
			AND desired_payload->'card'->>'header' = '📋 Codex · Plan 已完成'`,
		guildID, threadID, "conversation-reply:"+conversationID.String()+":message:%")
	if err != nil {
		return err
	}
	type planProjection struct {
		key       string
		messageID string
	}
	var plans []planProjection
	for rows.Next() {
		var plan planProjection
		if err := rows.Scan(&plan.key, &plan.messageID); err != nil {
			_ = rows.Close()
			return err
		}
		plans = append(plans, plan)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, plan := range plans {
		if plan.messageID != "" {
			if err := enqueueDiscordOutbox(ctx, tx,
				"plan-expire:"+startedRunID.String()+":"+plan.key, "message.delete",
				"channels/"+threadID+"/messages/"+plan.messageID,
				map[string]any{"channelId": threadID, "messageId": plan.messageID}, ""); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM discord_projections
			WHERE guild_id=$1 AND projection_key=$2`, guildID, plan.key); err != nil {
			return err
		}
	}
	return tx.Commit()
}
