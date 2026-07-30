package discordintegration

import (
	"context"

	"github.com/google/uuid"
)

// refreshConversationRunningTags 周期重投影，修复漏投以及人工移除 Running Tag。
func (d *Daemon) refreshConversationRunningTags(ctx context.Context, guildID string) error {
	rows, err := d.manager.db.QueryContext(ctx, `SELECT conversation.id, conversation.thread_id,
		EXISTS(SELECT 1 FROM codex_turn_intents intent
			WHERE intent.discord_conversation_id = conversation.id
			AND (intent.status IN ('placement_pending','queued','dispatching',
				'awaiting_confirmation','running','reconciling','retry_wait')
				OR (intent.operation = 'replace_last_turn'
					AND COALESCE(intent.replacement_phase,'reserved') <> 'terminal')))
		FROM discord_conversations conversation
		JOIN discord_forums forum ON forum.id = conversation.forum_id
		WHERE conversation.guild_id = $1 AND forum.forum_type = 'development'
			AND forum.binding_status = 'active'`, guildID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	outbox := NewSQLoutbox(d.manager.db)
	for rows.Next() {
		var conversationID uuid.UUID
		var threadID string
		var active bool
		if err := rows.Scan(&conversationID, &threadID, &active); err != nil {
			return err
		}
		if err := outbox.Enqueue(ctx, "conversation-running-tag:"+conversationID.String(),
			"thread.tag.toggle", "channels/"+threadID+"/tags/Running", map[string]any{
				"channelId": threadID, "tagName": "Running", "enabled": active,
			}, ""); err != nil {
			return err
		}
	}
	return rows.Err()
}
