package discordintegration

import (
	"context"
	"database/sql"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

const maxDiscussionMessages = 200

type pendingDiscussionMessage struct {
	ID          string
	DisplayName string
	Username    string
	Body        string
	ReceivedAt  time.Time
	Sequence    int64
}

func (s *ConversationService) enqueuePendingMessages(ctx context.Context, tx *sql.Tx,
	conversationID uuid.UUID, triggerMessageID string,
) error {
	rows, err := tx.QueryContext(ctx, `SELECT message_id, display_name, username, body, received_at
		FROM discord_input_messages
		WHERE conversation_id = $1 AND status = 'received' AND turn_intent_id IS NULL
		ORDER BY received_at DESC, message_id DESC LIMIT $2`, conversationID,
		maxDiscussionMessages+1)
	if err != nil {
		return err
	}
	var newest []pendingDiscussionMessage
	for rows.Next() {
		var message pendingDiscussionMessage
		if err := rows.Scan(&message.ID, &message.DisplayName, &message.Username,
			&message.Body, &message.ReceivedAt); err != nil {
			_ = rows.Close()
			return err
		}
		newest = append(newest, message)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(newest) == 0 {
		return nil
	}
	if len(newest) > maxDiscussionMessages {
		oldestIncluded := newest[maxDiscussionMessages-1]
		_, err = tx.ExecContext(ctx, `UPDATE discord_input_messages SET status = 'skipped',
			processed_at = now() WHERE conversation_id = $1 AND status = 'received'
			AND turn_intent_id IS NULL AND (received_at, message_id) < ($2, $3)`,
			conversationID, oldestIncluded.ReceivedAt, oldestIncluded.ID)
		if err != nil {
			return err
		}
		newest = newest[:maxDiscussionMessages]
	}

	messages := make([]pendingDiscussionMessage, len(newest))
	ids := make([]string, len(newest))
	for index := range newest {
		message := newest[len(newest)-1-index]
		messages[index] = message
		ids[index] = message.ID
	}
	if err := s.enqueueMessage(ctx, tx, conversationID, triggerMessageID); err != nil {
		return err
	}
	var intentID uuid.UUID
	if err := tx.QueryRowContext(ctx, `SELECT turn_intent_id FROM discord_input_messages
		WHERE message_id = $1`, triggerMessageID).Scan(&intentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE discord_input_messages SET turn_intent_id = $2
		WHERE message_id = ANY($1) AND turn_intent_id IS NULL`, pq.Array(ids), intentID); err != nil {
		return err
	}

	instruction := discussionInstruction(messages)
	var attachmentCount int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM discord_attachments attachment
		JOIN discord_input_messages message ON message.message_id = attachment.message_id
		WHERE message.turn_intent_id = $1`, intentID).Scan(&attachmentCount); err != nil {
		return err
	}
	if attachmentCount > DefaultMaxAttachments {
		instruction += fmt.Sprintf("\n\n[附件说明：本批次共有 %d 个附件，仅携带时间最新的 %d 个。]",
			attachmentCount, DefaultMaxAttachments)
	}
	_, err = tx.ExecContext(ctx, `UPDATE codex_turn_intents SET instruction = $2,
		updated_at = now() WHERE id = $1`, intentID, instruction)
	return err
}

func discussionInstruction(messages []pendingDiscussionMessage) string {
	if len(messages) == 1 {
		return messages[0].Body
	}
	var result strings.Builder
	result.WriteString("以下是 Discord 中自上次已提交输入后积累的讨论，消息按时间升序排列。\n<discord_discussion>\n")
	for _, message := range messages {
		name := strings.TrimSpace(message.DisplayName)
		if name == "" {
			name = message.Username
		}
		_, _ = fmt.Fprintf(&result, "  <message id=\"%s\" author=\"%s\" timestamp=\"%s\">\n    %s\n  </message>\n",
			html.EscapeString(message.ID), html.EscapeString(name),
			message.ReceivedAt.UTC().Format(time.RFC3339Nano), html.EscapeString(message.Body))
	}
	result.WriteString("</discord_discussion>")
	return result.String()
}
