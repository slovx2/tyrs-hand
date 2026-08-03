package discordintegration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
)

const EarlierMessageEditNotice = "编辑更早的消息无法触发 Codex rollback；只有当前最新的已提交用户输入可以重跑。"

type MessageEditOutcome string

const (
	MessageEditIgnored   MessageEditOutcome = "ignored"
	MessageEditBuffered  MessageEditOutcome = "buffered"
	MessageEditReserved  MessageEditOutcome = "reserved"
	MessageEditCoalesced MessageEditOutcome = "coalesced"
	MessageEditNotLatest MessageEditOutcome = "not_latest"
)

// HandleMessageEdit 更新持久化正文，并在目标是最新已提交输入时原子预留 replacement。
func (s *ConversationService) HandleMessageEdit(ctx context.Context, guildID, threadID,
	messageID, discordUserID, body string, editedAt time.Time,
) (MessageEditOutcome, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MessageEditIgnored, err
	}
	defer func() { _ = tx.Rollback() }()

	var conversationID uuid.UUID
	var persistedUserID, oldBody string
	var targetIntentID sql.NullString
	var revision int64
	err = tx.QueryRowContext(ctx, `SELECT message.conversation_id, message.discord_user_id,
		message.body, message.turn_intent_id::text, message.edit_revision
		FROM discord_input_messages message
		JOIN discord_conversations conversation ON conversation.id = message.conversation_id
		WHERE conversation.guild_id = $1 AND conversation.thread_id = $2
			AND message.message_id = $3 FOR UPDATE OF message`, guildID, threadID, messageID).
		Scan(&conversationID, &persistedUserID, &oldBody, &targetIntentID, &revision)
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
		editedAt = time.Now()
	}
	revision++
	if _, err = tx.ExecContext(ctx, `UPDATE discord_input_messages
		SET body = $2, edited_at = $3, edit_revision = $4 WHERE message_id = $1`,
		messageID, body, editedAt, revision); err != nil {
		return MessageEditIgnored, err
	}
	if !targetIntentID.Valid {
		return MessageEditBuffered, tx.Commit()
	}

	targetID, err := uuid.Parse(targetIntentID.String)
	if err != nil {
		return MessageEditIgnored, err
	}
	var controlID, repositoryID, projectID, profileID uuid.UUID
	var sequence int64
	var operation, status, primaryMessageID, projectionAnchor string
	var skillsJSON, toolsJSON, dangerousJSON []byte
	err = tx.QueryRowContext(ctx, `SELECT intent.control_id, intent.sequence_no, intent.operation,
		intent.status, COALESCE(intent.discord_message_id,''),
		COALESCE(intent.projection_anchor, intent.discord_message_id, 'desktop-' || intent.id::text),
		COALESCE(intent.repository_id, '00000000-0000-0000-0000-000000000000'::uuid),
		COALESCE(intent.workspace_project_id, '00000000-0000-0000-0000-000000000000'::uuid),
		intent.agent_profile_id, intent.skills, intent.allowed_tools, intent.dangerous_actions
		FROM codex_turn_intents intent WHERE intent.id = $1 FOR UPDATE`, targetID).
		Scan(&controlID, &sequence, &operation, &status, &primaryMessageID, &projectionAnchor,
			&repositoryID, &projectID, &profileID, &skillsJSON, &toolsJSON, &dangerousJSON)
	if err != nil {
		return MessageEditIgnored, err
	}
	var latestID uuid.UUID
	err = tx.QueryRowContext(ctx, `SELECT id FROM codex_turn_intents
		WHERE control_id = $1 AND operation IN ('turn_input','replace_last_turn')
		ORDER BY sequence_no DESC LIMIT 1`, controlID).Scan(&latestID)
	if err != nil {
		return MessageEditIgnored, err
	}
	if latestID != targetID || primaryMessageID != messageID {
		if err := tx.Commit(); err != nil {
			return MessageEditIgnored, err
		}
		return MessageEditNotLatest, nil
	}

	coalesce := status == "placement_pending" || status == "queued" || status == "retry_wait"
	replacementID := targetID
	if !coalesce {
		var skills, tools, dangerous []string
		if err := json.Unmarshal(skillsJSON, &skills); err != nil {
			return MessageEditIgnored, err
		}
		if err := json.Unmarshal(toolsJSON, &tools); err != nil {
			return MessageEditIgnored, err
		}
		if err := json.Unmarshal(dangerousJSON, &dangerous); err != nil {
			return MessageEditIgnored, err
		}
		var actorLogin, actorPermission, actorDisplayName string
		var actorParticipantID uuid.UUID
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(github_login,''), access_snapshot,
			participant_id, display_name FROM discord_input_messages WHERE message_id = $1`, messageID).
			Scan(&actorLogin, &actorPermission, &actorParticipantID, &actorDisplayName); err != nil {
			return MessageEditIgnored, err
		}
		replacementID, _, err = codexcontrol.NewRepository(s.db, 0).Enqueue(ctx, tx,
			codexcontrol.EnqueueRequest{
				SourceType: codexcontrol.SourceWorkspace, DiscordConversationID: conversationID,
				DiscordMessageID: messageID, RepositoryID: repositoryID, ProjectID: projectID,
				AgentProfileID: profileID, IdempotencyKey: fmt.Sprintf("discord:message-edit:%s:%d", messageID, revision),
				Skills: skills, AllowedTools: tools, DangerousActions: dangerous,
				ActorLogin: actorLogin, ActorPermission: actorPermission,
				ActorParticipantID: actorParticipantID, ActorDisplayName: actorDisplayName,
				ReplyPolicy: "silent", Operation: "replace_last_turn", Behavior: "start_when_idle",
				TargetIntentID: targetID, ProjectionAnchor: projectionAnchor,
				MessageEditRevision: revision, ReplacementPhase: "reserved",
			})
		if err != nil {
			return MessageEditIgnored, err
		}
	}
	if err := s.freezeReplacementMessages(ctx, tx, conversationID, targetID, replacementID,
		messageID, revision); err != nil {
		return MessageEditIgnored, err
	}
	if err := tx.Commit(); err != nil {
		return MessageEditIgnored, err
	}
	s.notifyJobs(ctx)
	if coalesce {
		return MessageEditCoalesced, nil
	}
	return MessageEditReserved, nil
}

func (s *ConversationService) freezeReplacementMessages(ctx context.Context, tx *sql.Tx,
	conversationID, targetID, replacementID uuid.UUID, editedMessageID string, revision int64,
) error {
	rows, err := tx.QueryContext(ctx, `(
		SELECT message.message_id, message.display_name, message.username, message.body,
			message.received_at, replay.sequence_no, 0 AS batch
		FROM discord_input_messages message JOIN codex_turn_intents replay
			ON replay.id=message.turn_intent_id WHERE message.turn_intent_id IN (
			SELECT replay.id FROM codex_turn_intents target
			JOIN codex_turn_intents replay ON replay.control_id=target.control_id
				AND replay.operation IN ('turn_input','replace_last_turn')
				AND replay.sequence_no<=target.sequence_no
				AND (replay.id=target.id OR (target.confirmed_codex_turn_id IS NOT NULL
					AND replay.confirmed_codex_turn_id=target.confirmed_codex_turn_id))
			WHERE target.id=$2
		)
	) UNION ALL (
		SELECT message_id, display_name, username, body, received_at,
			$4::bigint AS sequence_no, 1 AS batch FROM (
			SELECT message_id, display_name, username, body, received_at
			FROM discord_input_messages WHERE conversation_id = $1 AND status = 'received'
				AND turn_intent_id IS NULL ORDER BY received_at DESC, message_id DESC LIMIT $3
		) pending
	)`, conversationID, targetID, maxDiscussionMessages, int64(math.MaxInt64))
	if err != nil {
		return err
	}
	var messages []pendingDiscussionMessage
	var ids []string
	var oldestPending *pendingDiscussionMessage
	for rows.Next() {
		var message pendingDiscussionMessage
		var batch int
		if err := rows.Scan(&message.ID, &message.DisplayName, &message.Username, &message.Body,
			&message.ReceivedAt, &message.Sequence, &batch); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, message.ID)
		messages = append(messages, message)
		if batch == 1 && (oldestPending == nil || message.ReceivedAt.Before(oldestPending.ReceivedAt) ||
			(message.ReceivedAt.Equal(oldestPending.ReceivedAt) && message.ID < oldestPending.ID)) {
			copyMessage := message
			oldestPending = &copyMessage
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(messages) == 0 {
		return errors.New("replacement 没有可重放的 Discord 输入")
	}
	if oldestPending != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE discord_input_messages SET status='skipped',
			processed_at=now() WHERE conversation_id=$1 AND status='received'
			AND turn_intent_id IS NULL AND (received_at,message_id)<($2,$3)`,
			conversationID, oldestPending.ReceivedAt, oldestPending.ID); err != nil {
			return err
		}
	}
	desktopRows, err := tx.QueryContext(ctx, `SELECT replay.id, replay.instruction,
		replay.sequence_no, replay.created_at
		FROM codex_turn_intents target JOIN codex_turn_intents replay
		ON replay.control_id=target.control_id AND replay.input_surface='desktop'
			AND replay.operation IN ('turn_input','replace_last_turn')
			AND replay.sequence_no<=target.sequence_no
			AND (replay.id=target.id OR (target.confirmed_codex_turn_id IS NOT NULL
				AND replay.confirmed_codex_turn_id=target.confirmed_codex_turn_id))
		WHERE target.id=$1`, targetID)
	if err != nil {
		return err
	}
	for desktopRows.Next() {
		var message pendingDiscussionMessage
		var intentID uuid.UUID
		if err := desktopRows.Scan(&intentID, &message.Body, &message.Sequence,
			&message.ReceivedAt); err != nil {
			_ = desktopRows.Close()
			return err
		}
		message.ID = "desktop-" + intentID.String()
		message.DisplayName = "Desktop"
		message.Username = "Desktop"
		messages = append(messages, message)
	}
	if err := desktopRows.Close(); err != nil {
		return err
	}
	sort.SliceStable(messages, func(left, right int) bool {
		if messages[left].Sequence != messages[right].Sequence {
			return messages[left].Sequence < messages[right].Sequence
		}
		if !messages[left].ReceivedAt.Equal(messages[right].ReceivedAt) {
			return messages[left].ReceivedAt.Before(messages[right].ReceivedAt)
		}
		return messages[left].ID < messages[right].ID
	})
	realDiscordMessages := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		realDiscordMessages[id] = struct{}{}
	}
	primaryMessageID := editedMessageID
	for _, message := range messages {
		if _, ok := realDiscordMessages[message.ID]; ok {
			primaryMessageID = message.ID
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE discord_input_messages
		SET replacement_previous_intent_id = CASE
			WHEN turn_intent_id = $2 THEN replacement_previous_intent_id ELSE turn_intent_id END,
			turn_intent_id = $2 WHERE message_id = ANY($1)`, pq.Array(ids), replacementID); err != nil {
		return err
	}
	instruction := discussionInstruction(messages)
	var attachmentCount int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM discord_attachments attachment
		JOIN discord_input_messages message ON message.message_id = attachment.message_id
		WHERE message.turn_intent_id = $1`, replacementID).Scan(&attachmentCount); err != nil {
		return err
	}
	if attachmentCount > DefaultMaxAttachments {
		instruction += fmt.Sprintf("\n\n[附件说明：本批次共有 %d 个附件，仅携带时间最新的 %d 个。]",
			attachmentCount, DefaultMaxAttachments)
	}
	_, err = tx.ExecContext(ctx, `UPDATE codex_turn_intents SET instruction = $2,
		discord_message_id = $3, message_edit_revision = $4,
		replacement_phase = CASE WHEN operation = 'replace_last_turn' THEN 'reserved'
			ELSE replacement_phase END,
		updated_at = now() WHERE id = $1`, replacementID, instruction, primaryMessageID, revision)
	return err
}
