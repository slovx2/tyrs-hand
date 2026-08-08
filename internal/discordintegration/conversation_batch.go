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
		WHERE conversation_id = $1 AND status = 'received' AND official_submission_id IS NULL
		ORDER BY received_at DESC, message_id DESC LIMIT $2::integer`, conversationID,
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
			AND official_submission_id IS NULL
			AND (received_at, message_id) < ($2::timestamptz, $3::text)`,
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
	var submissionID uuid.UUID
	if err := tx.QueryRowContext(ctx, `SELECT official_submission_id FROM discord_input_messages
		WHERE message_id = $1`, triggerMessageID).Scan(&submissionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE discord_input_messages
		SET official_submission_id=$2::uuid WHERE message_id=ANY($1::text[])
		AND official_submission_id IS NULL`, pq.Array(ids), submissionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM official_submission_attachments
		WHERE submission_id=$1`, submissionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO official_submission_attachments(
		submission_id,attachment_id,ordinal)
		SELECT $1,attachment.id,row_number() OVER(
			ORDER BY message.received_at,attachment.created_at,attachment.id)-1
		FROM discord_input_messages message
		JOIN discord_attachments attachment ON attachment.message_id=message.message_id
		WHERE message.official_submission_id=$1 AND attachment.status='ready'
		  AND attachment.storage_key IS NOT NULL
		ORDER BY message.received_at,attachment.created_at,attachment.id LIMIT 10`,
		submissionID); err != nil {
		return err
	}

	instruction := discussionInstruction(messages)
	var attachmentCount int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM discord_attachments attachment
		JOIN discord_input_messages message ON message.message_id = attachment.message_id
		WHERE message.official_submission_id = $1`, submissionID).Scan(&attachmentCount); err != nil {
		return err
	}
	if attachmentCount > DefaultMaxAttachments {
		instruction += fmt.Sprintf("\n\n[附件说明：本批次共有 %d 个附件，仅携带时间最新的 %d 个。]",
			attachmentCount, DefaultMaxAttachments)
	}
	_, err = tx.ExecContext(ctx, `UPDATE official_turn_submissions SET instruction=$2::text,
		input=jsonb_build_array(jsonb_build_object('type','text','text',$2::text,
			'text_elements','[]'::jsonb)),updated_at=now() WHERE id=$1`, submissionID,
		instruction)
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
