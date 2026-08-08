package discordintegration

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

const EarlierMessageEditNotice = "该消息已经进入官方 Codex 历史；编辑只更新 Discord 原文，不会回滚或重放 Turn。"

type MessageEditOutcome string

const (
	MessageEditIgnored   MessageEditOutcome = "ignored"
	MessageEditBuffered  MessageEditOutcome = "buffered"
	MessageEditCoalesced MessageEditOutcome = "coalesced"
	MessageEditNotLatest MessageEditOutcome = "not_latest"
)

func (s *ConversationService) HandleMessageEdit(ctx context.Context, guildID, threadID,
	messageID, discordUserID, body string, editedAt time.Time,
) (MessageEditOutcome, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MessageEditIgnored, err
	}
	defer func() { _ = tx.Rollback() }()
	var persistedUserID, oldBody string
	var submissionID uuid.NullUUID
	var revision int64
	err = tx.QueryRowContext(ctx, `SELECT message.discord_user_id,message.body,
		message.official_submission_id,message.edit_revision
		FROM discord_input_messages message
		JOIN discord_conversations conversation ON conversation.id=message.conversation_id
		WHERE conversation.guild_id=$1 AND conversation.thread_id=$2
		AND message.message_id=$3 FOR UPDATE OF message`, guildID, threadID, messageID).
		Scan(&persistedUserID, &oldBody, &submissionID, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return MessageEditIgnored, tx.Commit()
	}
	if err != nil {
		return MessageEditIgnored, err
	}
	if persistedUserID != discordUserID || oldBody == body {
		return MessageEditIgnored, tx.Commit()
	}
	if editedAt.IsZero() {
		editedAt = time.Now().UTC()
	}
	revision++
	if _, err = tx.ExecContext(ctx, `UPDATE discord_input_messages SET body=$2,
		edited_at=$3,edit_revision=$4 WHERE message_id=$1`, messageID, body, editedAt,
		revision); err != nil {
		return MessageEditIgnored, err
	}
	if !submissionID.Valid {
		return MessageEditBuffered, tx.Commit()
	}
	var status string
	err = tx.QueryRowContext(ctx, `SELECT status FROM official_turn_submissions
		WHERE id=$1 FOR UPDATE`, submissionID.UUID).Scan(&status)
	if err != nil {
		return MessageEditIgnored, err
	}
	if status != "queued" {
		if err = tx.Commit(); err != nil {
			return MessageEditIgnored, err
		}
		if status == "submitting" || status == "ambiguous" || status == "submitted" {
			return MessageEditNotLatest, nil
		}
		return MessageEditIgnored, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT message_id,display_name,username,body,
		received_at FROM discord_input_messages WHERE official_submission_id=$1
		ORDER BY received_at,message_id`, submissionID.UUID)
	if err != nil {
		return MessageEditIgnored, err
	}
	messages := make([]pendingDiscussionMessage, 0)
	for rows.Next() {
		var message pendingDiscussionMessage
		if err = rows.Scan(&message.ID, &message.DisplayName, &message.Username,
			&message.Body, &message.ReceivedAt); err != nil {
			_ = rows.Close()
			return MessageEditIgnored, err
		}
		messages = append(messages, message)
	}
	if err = rows.Close(); err != nil {
		return MessageEditIgnored, err
	}
	instruction := discussionInstruction(messages)
	_, err = tx.ExecContext(ctx, `UPDATE official_turn_submissions SET instruction=$2,
		input=jsonb_build_array(jsonb_build_object('type','text','text',$2,
			'text_elements','[]'::jsonb)),updated_at=now() WHERE id=$1 AND status='queued'`,
		submissionID.UUID, instruction)
	if err != nil {
		return MessageEditIgnored, err
	}
	if err = tx.Commit(); err != nil {
		return MessageEditIgnored, err
	}
	return MessageEditCoalesced, nil
}
