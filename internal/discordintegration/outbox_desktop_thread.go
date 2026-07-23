package discordintegration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
)

type desktopPostResult struct {
	ThreadID  string `json:"threadId"`
	MessageID string `json:"messageId"`
}

func (s *SQLoutbox) completeDesktopThreadPost(ctx context.Context, tx *sql.Tx,
	item OutboxItem, response json.RawMessage,
) error {
	requestID, err := uuid.Parse(strings.TrimPrefix(item.OperationKey, "desktop-thread-post:"))
	if err != nil {
		return errors.New("desktop thread Post operation key 无效")
	}
	var result desktopPostResult
	if json.Unmarshal(response, &result) != nil || result.ThreadID == "" || result.MessageID == "" {
		return errors.New("desktop thread Post Outbox 结果无效")
	}
	var status, guildID, ownerID, sshUserID, sshDisplayName, previewTitle, desiredName string
	var desiredSource, firstProjectionKey, firstInputText string
	var environmentID, forumID, repositoryID, profileID, controlID uuid.UUID
	var contextVersion, desiredRevision, lifecycleRevision int64
	var lifecycleState string
	var model, effort sql.NullString
	var serviceTier string
	err = tx.QueryRowContext(ctx, `SELECT r.status, r.environment_id, r.forum_id,
		r.control_id, f.guild_id, f.owner_discord_user_id,
		COALESCE(NULLIF(r.first_input_actor_discord_user_id,''),
			e.ssh_discord_user_id, ''),
		COALESCE(NULLIF(r.first_input_actor_display_name,''),
			NULLIF(member.display_name,''), member.username, ''),
		f.repository_id, ct.agent_profile_id, ct.context_version, ct.model,
		ct.reasoning_effort, COALESCE(ct.service_tier,''),
		COALESCE(r.preview_title,''), COALESCE(ct.desired_thread_name,''),
		COALESCE(ct.desired_thread_name_source,''), ct.desired_thread_name_revision,
		COALESCE(r.first_input_projection_key,''), COALESCE(r.first_input_text,''),
		ct.lifecycle_state, ct.lifecycle_revision
		FROM desktop_thread_requests r JOIN discord_forums f ON f.id = r.forum_id
		JOIN discord_development_environments e ON e.id = r.environment_id
		JOIN codex_thread_controls ct ON ct.id = r.control_id
		LEFT JOIN discord_members member ON member.guild_id = e.guild_id
			AND member.discord_user_id = COALESCE(
				NULLIF(r.first_input_actor_discord_user_id,''), e.ssh_discord_user_id)
		WHERE r.id = $1 FOR UPDATE OF r, ct`, requestID).Scan(&status, &environmentID, &forumID,
		&controlID, &guildID, &ownerID, &sshUserID, &sshDisplayName, &repositoryID, &profileID,
		&contextVersion, &model, &effort, &serviceTier, &previewTitle, &desiredName,
		&desiredSource, &desiredRevision, &firstProjectionKey, &firstInputText,
		&lifecycleState, &lifecycleRevision)
	if err != nil {
		return err
	}
	if status == "completed" {
		return nil
	}
	if status != "post_pending" {
		return errors.New("desktop thread Post reservation 状态无效")
	}
	title := previewTitle
	if desiredName != "" && desiredSource == "codex" {
		title = desiredName
	}
	if title == "" {
		title = "Desktop"
	}
	if sshUserID != "" {
		ownerID = sshUserID
	}
	conversationID := uuid.New()
	_, err = tx.ExecContext(ctx, `INSERT INTO discord_conversations
		(id, guild_id, forum_id, thread_id, starter_message_id, owner_discord_user_id,
		 repository_id, agent_profile_id, title, status, model, reasoning_effort, service_tier,
		 configuration_status, configured_by_discord_user_id, title_rename_status,
		 lifecycle_state, lifecycle_revision)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'active',NULLIF($10,''),NULLIF($11,''),NULLIF($12,''),
			'configured',$6,'skipped',$13,$14)`, conversationID, guildID, forumID, result.ThreadID,
		result.MessageID, ownerID, repositoryID, profileID, title,
		model.String, effort.String, serviceTier, lifecycleState, lifecycleRevision)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE codex_thread_controls SET
		discord_conversation_id = $2,
		applied_thread_name = CASE
			WHEN $3 = 'codex' AND $4 <> '' AND $4 = $5 THEN $4
			ELSE applied_thread_name END,
		applied_thread_name_revision = CASE
			WHEN $3 = 'codex' AND $4 <> '' AND $4 = $5 THEN $6
			ELSE applied_thread_name_revision END,
		updated_at = now() WHERE id = $1`,
		controlID, conversationID, desiredSource, desiredName, previewTitle, desiredRevision)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE codex_turn_intents SET
		discord_conversation_id = $2,
		desktop_input_projection_status = CASE
			WHEN desktop_input_projection_key = (SELECT first_input_projection_key
				FROM desktop_thread_requests WHERE id = $3) THEN 'projected'
			ELSE desktop_input_projection_status END,
		updated_at = now() WHERE control_id = $1 AND input_surface = 'desktop'`,
		controlID, conversationID, requestID)
	if err != nil {
		return err
	}
	updated, err := tx.ExecContext(ctx, `UPDATE desktop_thread_requests SET
		status = 'completed', conversation_id = $2, updated_at = now()
		WHERE id = $1 AND status = 'post_pending'`, requestID, conversationID)
	if err != nil {
		return err
	}
	changed, _ := updated.RowsAffected()
	if changed != 1 {
		return errors.New("desktop thread Post 状态已被并发修改")
	}
	if desiredName != "" && desiredName != previewTitle {
		if err := EnqueueThreadName(ctx, tx, controlID, result.ThreadID,
			desiredName, desiredRevision); err != nil {
			return err
		}
	}
	if err := EnqueueDesktopInputPages(ctx, tx, result.ThreadID, conversationID,
		firstProjectionKey, sshDisplayName, firstInputText, 1); err != nil {
		return err
	}
	if err := enqueuePendingDesktopInputs(ctx, tx, controlID, result.ThreadID,
		conversationID, firstProjectionKey); err != nil {
		return err
	}
	if sshUserID != "" {
		if err := enqueueDiscordOutbox(ctx, tx,
			"desktop-thread-member:"+requestID.String(), "thread.member.add",
			"channels/"+result.ThreadID+"/thread-members/"+sshUserID,
			map[string]any{"channelId": result.ThreadID, "userId": sshUserID,
				"conversationId": conversationID.String()}, ""); err != nil {
			return err
		}
	}
	if lifecycleState == "active" || lifecycleState == "archived" {
		if err := EnqueueConversationLifecycleTx(ctx, tx, conversationID); err != nil {
			return err
		}
	}
	_ = environmentID
	_ = contextVersion
	return nil
}

func enqueuePendingDesktopInputs(ctx context.Context, tx *sql.Tx, controlID uuid.UUID,
	threadID string, conversationID uuid.UUID, firstProjectionKey string,
) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, desktop_input_projection_key,
		COALESCE(actor_display_name,''), instruction
		FROM codex_turn_intents
		WHERE control_id = $1 AND input_surface = 'desktop'
			AND desktop_input_projection_key IS NOT NULL
			AND desktop_input_projection_key <> $2
		ORDER BY sequence_no, id`, controlID, firstProjectionKey)
	if err != nil {
		return err
	}
	type pendingInput struct {
		id          uuid.UUID
		key         string
		displayName string
		input       string
	}
	inputs := make([]pendingInput, 0)
	for rows.Next() {
		var item pendingInput
		if err := rows.Scan(&item.id, &item.key, &item.displayName, &item.input); err != nil {
			_ = rows.Close()
			return err
		}
		inputs = append(inputs, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range inputs {
		if err := EnqueueDesktopInputPages(ctx, tx, threadID, conversationID,
			item.key, item.displayName, item.input, 0); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE codex_turn_intents SET
			desktop_input_projection_status = 'projected', updated_at = now()
			WHERE id = $1`, item.id); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLoutbox) replayDesktopProjection(ctx context.Context, requestID uuid.UUID) error {
	var controlID, conversationID uuid.UUID
	var guildID, threadID string
	err := s.db.QueryRowContext(ctx, `SELECT r.control_id, r.conversation_id, c.guild_id, c.thread_id
		FROM desktop_thread_requests r JOIN discord_conversations c ON c.id = r.conversation_id
		WHERE r.id = $1 AND r.status = 'completed'`, requestID).
		Scan(&controlID, &conversationID, &guildID, &threadID)
	if err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT i.id, r.id, r.status,
		COALESCE(i.result->>'finalAnswer','')
		FROM codex_turn_intents i
		JOIN codex_turn_runs r ON r.primary_intent_id = i.id
		WHERE i.control_id = $1 AND i.input_surface = 'desktop'
		ORDER BY i.sequence_no`, controlID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	type projection struct {
		intentID uuid.UUID
		runID    uuid.UUID
		status   string
		answer   string
	}
	var projections []projection
	for rows.Next() {
		var item projection
		if err := rows.Scan(&item.intentID, &item.runID, &item.status, &item.answer); err != nil {
			return err
		}
		projections = append(projections, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, item := range projections {
		anchor := "desktop-" + item.intentID.String()
		if item.status == "completed" {
			if err := ProjectConversationStatus(ctx, s.db, guildID, threadID, conversationID,
				anchor, item.runID, ConversationCompleted, "本轮处理完成。"); err != nil {
				return err
			}
			if item.answer != "" {
				if err := ProjectConversationReply(ctx, s.db, threadID, conversationID,
					anchor, item.answer); err != nil {
					return err
				}
			}
			continue
		}
		if item.status == "starting" || item.status == "running" ||
			item.status == "waiting_for_user" || item.status == "reconciling" {
			if err := ProjectConversationStatus(ctx, s.db, guildID, threadID, conversationID,
				anchor, item.runID, ConversationRunning, "正在处理请求。"); err != nil {
				return err
			}
		}
	}
	return nil
}
