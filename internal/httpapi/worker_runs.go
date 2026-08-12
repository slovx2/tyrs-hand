package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/codexsettings"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

func (s *Server) workerRunHeartbeat(c *gin.Context) {
	var request workerprotocol.RunLeaseRequest
	runID, worker, ok := requireRunLease(c, &request)
	if !ok {
		return
	}
	claimed, err := s.claimedRemoteRun(c.Request.Context(), worker.ID, runID, request)
	if err == nil {
		err = codexcontrol.NewRepository(s.db, s.cfg.LeaseDuration).Heartbeat(c.Request.Context(), claimed)
	}
	if err != nil {
		remoteRunError(c, "远程任务续租失败", err)
		return
	}
	commands, err := s.pendingRunCommands(c.Request.Context(), claimed)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取远程 Run 指令失败", err)
		return
	}
	c.JSON(http.StatusOK, workerprotocol.RunHeartbeatResponse{Commands: commands,
		Recovery: workerprotocol.RunRecoveryState{Recovering: claimed.Recovering,
			SubmissionID: claimed.SubmissionID, ConfirmedTurnID: claimed.ConfirmedTurnID,
			ExternalThreadID: claimed.ExternalThreadID}})
}

func (s *Server) pendingRunCommands(ctx context.Context,
	claimed *codexcontrol.ClaimedControl,
) ([]workerprotocol.RunCommand, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, sequence_no, operation, instruction,
		COALESCE(discord_message_id,'') FROM codex_turn_intents
		WHERE control_id = $1 AND sequence_no > $2 AND status IN ('queued','retry_wait')
		ORDER BY sequence_no LIMIT 5`, claimed.ControlID, claimed.Sequence)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var commands []workerprotocol.RunCommand
	for rows.Next() {
		var command workerprotocol.RunCommand
		var messageID string
		if err := rows.Scan(&command.ID, &command.Sequence, &command.Operation,
			&command.Instruction, &messageID); err != nil {
			return nil, err
		}
		if claimed.SourceType == codexcontrol.SourceWorkspace {
			copyClaimed := *claimed
			copyClaimed.ID, copyClaimed.Sequence = command.ID, command.Sequence
			copyClaimed.InputSurface = "client"
			if messageID != "" {
				copyClaimed.InputSurface = "discord"
			}
			copyClaimed.DiscordMessageID = messageID
			copyClaimed.Instruction = command.Instruction
			command.Session, err = s.loadWorkspaceWorkerSnapshot(ctx, &copyClaimed)
			if err != nil {
				return nil, err
			}
			if messageID != "" && claimed.DiscordConversationID != uuid.Nil {
				command.Discord, err = s.loadDiscordWorkerSnapshot(ctx, &copyClaimed)
				if err != nil {
					return nil, err
				}
			}
		}
		commands = append(commands, command)
	}
	return commands, rows.Err()
}

func (s *Server) workerCommandAck(c *gin.Context) {
	var request workerprotocol.CommandAckRequest
	runID, worker, ok := requireRunLease(c, &request)
	if !ok {
		return
	}
	claimed, err := s.claimedRemoteRun(c.Request.Context(), worker.ID, runID,
		request.RunLeaseRequest)
	if err != nil {
		remoteRunError(c, "校验远程 Run 指令确认失败", err)
		return
	}
	if request.Action != "steer" && request.Action != "interrupt" {
		badRequest(c, errors.New("run 指令确认 action 无效"))
		return
	}
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "确认远程 Run 指令失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var operation, status, discordMessageID, inputSurface string
	err = tx.QueryRowContext(c.Request.Context(), `SELECT operation, status,
		COALESCE(discord_message_id,''), COALESCE(input_surface,'')
		FROM codex_turn_intents WHERE id = $1 AND control_id = $2 FOR UPDATE`,
		request.CommandID, claimed.ControlID).Scan(&operation, &status, &discordMessageID, &inputSurface)
	if err != nil {
		remoteRunError(c, "Run 指令不存在", err)
		return
	}
	if status == "completed" || (status == "running" && request.Action == "steer") {
		c.Status(http.StatusNoContent)
		return
	}
	if operation != request.Action && (operation != "turn_input" || request.Action != "steer") &&
		(operation != "replace_last_turn" || request.Action != "interrupt") {
		badRequest(c, errors.New("run 指令确认与原操作不匹配"))
		return
	}
	if request.Action == "interrupt" {
		if operation != "replace_last_turn" {
			_, err = tx.ExecContext(c.Request.Context(), `UPDATE codex_turn_intents SET
				status = 'completed', resolved_action = 'interrupt', confirmed_codex_turn_id = $2,
				finished_at = now(), updated_at = now() WHERE id = $1`, request.CommandID,
				request.TurnID)
		}
	} else {
		_, err = tx.ExecContext(c.Request.Context(), `UPDATE codex_turn_intents SET
			status = 'running', resolved_action = 'steer', confirmed_codex_turn_id = $2,
			confirmed_at = now(), updated_at = now() WHERE id = $1`, request.CommandID,
			request.TurnID)
		if err == nil {
			_, err = tx.ExecContext(c.Request.Context(), `UPDATE codex_turn_runs SET
				append_count = append_count + 1 WHERE id = $1 AND append_count < max_append_count`,
				claimed.RunID)
		}
		if err == nil && claimed.SourceType == codexcontrol.SourceWorkspace &&
			claimed.SessionID != uuid.Nil {
			_, err = tx.ExecContext(c.Request.Context(), `UPDATE session_messages
				SET conversation_turn_id=$2,updated_at=now() WHERE turn_intent_id=$1`,
				request.CommandID, claimed.ID)
		}
		if err == nil && claimed.SourceType == codexcontrol.SourceWorkspace &&
			claimed.SessionID != uuid.Nil {
			payload, _ := json.Marshal(gin.H{"conversationTurnId": claimed.ID,
				"runId": claimed.RunID, "steerIntentId": request.CommandID})
			_, err = tx.ExecContext(c.Request.Context(), `INSERT INTO client_updates(
				session_id,update_type,entity_type,entity_id,payload)
				VALUES ($1,'conversation.turn.updated','turn',$2,$3)`, claimed.SessionID,
				claimed.ID.String(), payload)
		}
		if err == nil && claimed.SourceType == codexcontrol.SourceWorkspace &&
			claimed.DiscordConversationID != uuid.Nil {
			err = recordDiscordIntentContributors(c.Request.Context(), tx, claimed.RunID,
				claimed.DiscordConversationID, request.CommandID, request.TurnID)
		}
		if err == nil && claimed.SourceType == codexcontrol.SourceWorkspace &&
			claimed.DiscordConversationID != uuid.Nil {
			var guildID, threadID string
			err = tx.QueryRowContext(c.Request.Context(), `SELECT guild_id, thread_id
				FROM discord_conversations WHERE id=$1`, claimed.DiscordConversationID).
				Scan(&guildID, &threadID)
			if err == nil {
				if discordMessageID == "" && inputSurface == "desktop" {
					discordMessageID = "desktop-" + request.CommandID.String()
					err = discordintegration.ProjectConversationThinkingTx(c.Request.Context(), tx,
						guildID, threadID, claimed.DiscordConversationID, discordMessageID)
				}
			}
			if err == nil && discordMessageID != "" {
				err = discordintegration.RegisterConversationStatusSteerTx(c.Request.Context(), tx,
					claimed.RunID, claimed.DiscordConversationID, guildID, discordMessageID)
			}
		}
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "确认远程 Run 指令失败", err)
		return
	}
	if err := tx.Commit(); err != nil {
		problem(c, http.StatusInternalServerError, "提交远程 Run 指令确认失败", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) workerRunEvents(c *gin.Context) {
	var request workerprotocol.EventsRequest
	runID, worker, ok := requireRunLease(c, &request)
	if !ok {
		return
	}
	claimed, err := s.claimedRemoteRun(c.Request.Context(), worker.ID, runID, request.RunLeaseRequest)
	if err != nil {
		remoteRunError(c, "校验远程任务失败", err)
		return
	}
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "记录远程事件失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var lastSequence int64
	if err := tx.QueryRowContext(c.Request.Context(), `SELECT worker_event_sequence
		FROM codex_turn_runs WHERE id = $1 AND worker_id = $2 FOR UPDATE`,
		runID, worker.ID).Scan(&lastSequence); err != nil {
		problem(c, http.StatusInternalServerError, "锁定远程事件序列失败", err)
		return
	}
	for _, event := range request.Events {
		if event.Sequence <= 0 || event.Type == "" {
			badRequest(c, errors.New("远程事件缺少 sequence 或 type"))
			return
		}
		if event.Sequence <= lastSequence {
			continue
		}
		if event.Sequence != lastSequence+1 {
			badRequest(c, fmt.Errorf("远程事件序号不连续：当前 %d，收到 %d",
				lastSequence, event.Sequence))
			return
		}
		externalEventID := fmt.Sprintf("worker:%d", event.Sequence)
		var eventID int64
		var occurredAt time.Time
		err = tx.QueryRowContext(c.Request.Context(), `INSERT INTO agent_events
			(control_id, intent_id, run_id, event_type, external_event_id, payload,
			 run_event_sequence)
			VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(run_id, external_event_id)
			WHERE run_id IS NOT NULL AND external_event_id IS NOT NULL DO NOTHING
			RETURNING id,occurred_at`,
			claimed.ControlID, claimed.ID, claimed.RunID, event.Type,
			externalEventID, event.Payload, event.Sequence).Scan(&eventID, &occurredAt)
		if errors.Is(err, sql.ErrNoRows) {
			err = tx.QueryRowContext(c.Request.Context(), `SELECT id,occurred_at FROM agent_events
				WHERE run_id=$1 AND external_event_id=$2`, claimed.RunID, externalEventID).
				Scan(&eventID, &occurredAt)
		}
		if err != nil {
			problem(c, http.StatusInternalServerError, "记录远程事件失败", err)
			return
		}
		if err = discordintegration.ResolveConversationStatusBoundaryTx(c.Request.Context(), tx,
			claimed.RunID, eventID, event.Type, event.Payload); err != nil {
			problem(c, http.StatusInternalServerError, "解析过程卡分段边界失败", err)
			return
		}
		if err = projectRunEventTx(c.Request.Context(), tx, claimed.RunID, runEventProjection{
			Sequence: event.Sequence, Type: event.Type, Payload: event.Payload,
			OccurredAt: occurredAt,
		}); err != nil {
			problem(c, http.StatusInternalServerError, "投影移动端过程动态失败", err)
			return
		}
		if event.Type == "item/completed" {
			itemID, clientID, isUserMessage := completedUserMessage(event.Payload)
			if isUserMessage {
				if err := recordDesktopUserMessageItem(c.Request.Context(), tx,
					claimed, itemID, clientID); err != nil {
					problem(c, http.StatusInternalServerError, "确认 Desktop 用户消息失败", err)
					return
				}
				if claimed.SourceType == codexcontrol.SourceWorkspace && claimed.SessionID != uuid.Nil {
					var segmentID uuid.UUID
					err = tx.QueryRowContext(c.Request.Context(), `SELECT id FROM run_process_segments
						WHERE run_id=$1 ORDER BY sequence DESC LIMIT 1`, claimed.RunID).Scan(&segmentID)
					if err == nil {
						payload, _ := json.Marshal(gin.H{"conversationTurnId": claimed.ID,
							"runId": claimed.RunID, "segmentId": segmentID})
						_, err = tx.ExecContext(c.Request.Context(), `INSERT INTO client_updates(
							session_id,update_type,entity_type,entity_id,payload)
							VALUES ($1,'run.segment.updated','turn',$2,$3)`, claimed.SessionID,
							claimed.ID.String(), payload)
					}
					if err != nil {
						problem(c, http.StatusInternalServerError, "通知移动端过程分段失败", err)
						return
					}
				}
			}
		}
		if event.Type == "runtime.settings_applied" {
			if err := recordRuntimeSettingsApplied(c.Request.Context(), tx, claimed,
				event.Payload); err != nil {
				badRequest(c, err)
				return
			}
		}
		lastSequence = event.Sequence
	}
	if _, err := tx.ExecContext(c.Request.Context(), `UPDATE codex_turn_runs
		SET worker_event_sequence = $2,client_projection_sequence=$2 WHERE id = $1`,
		runID, lastSequence); err != nil {
		problem(c, http.StatusInternalServerError, "更新远程事件序列失败", err)
		return
	}
	if err := tx.Commit(); err != nil {
		problem(c, http.StatusInternalServerError, "提交远程事件失败", err)
		return
	}
	if claimed.SourceType == codexcontrol.SourceWorkspace && claimed.SessionID != uuid.Nil &&
		s.clientUpdateHub != nil {
		for _, event := range request.Events {
			sequence := event.Sequence
			payload, _ := json.Marshal(gin.H{"eventType": event.Type, "data": event.Payload})
			updateType := "agent.event"
			if event.Type == "item/agentMessage/delta" || event.Type == "item/delta" {
				updateType = "message.delta"
			}
			s.clientUpdateHub.publish(clientUpdate{
				Kind: "live", SessionID: &claimed.SessionID, Type: updateType,
				EntityType: "run", EntityID: claimed.RunID.String(), RunEventSeq: &sequence,
				Payload: payload, CreatedAt: time.Now().UTC(),
			})
		}
	}
	if claimed.SourceType == codexcontrol.SourceWorkspace {
		s.hydrateDesktopConversation(c.Request.Context(), claimed)
		hasExplicitProgress := false
		timelineChanged := false
		for _, event := range request.Events {
			if event.Type == "discord.progress" {
				hasExplicitProgress = true
				s.projectRemoteDiscordProgress(c.Request.Context(), claimed, event.Payload)
				continue
			}
			if event.Type == "item/started" || event.Type == "item/completed" ||
				event.Type == "item/agentMessage/delta" || event.Type == "item/delta" ||
				event.Type == "discord/tool/started" || event.Type == "discord/tool/completed" {
				timelineChanged = true
			}
		}
		if timelineChanged && !hasExplicitProgress {
			guildID, threadID, targetErr := s.discordProjectionTarget(c.Request.Context(), claimed)
			if targetErr == nil {
				_ = discordintegration.ProjectConversationStatus(c.Request.Context(), s.db,
					guildID, threadID, claimed.DiscordConversationID, discordProjectionAnchor(claimed),
					claimed.RunID, discordintegration.ConversationRunning, "正在处理请求。")
			}
		}
	}
	c.Status(http.StatusNoContent)
}

func recordRuntimeSettingsApplied(ctx context.Context, tx *sql.Tx,
	claimed *codexcontrol.ClaimedControl, payload json.RawMessage,
) error {
	var value workerprotocol.RuntimeSettingsApplied
	if err := json.Unmarshal(payload, &value); err != nil {
		return fmt.Errorf("runtime.settings_applied payload 无效: %w", err)
	}
	if value.Phase != "thread/start" && value.Phase != "thread/resume" &&
		value.Phase != "turn/start" {
		return errors.New("runtime.settings_applied phase 无效")
	}
	if value.ReasoningEffort != "" && value.ReasoningEffort != "low" &&
		value.ReasoningEffort != "medium" && value.ReasoningEffort != "high" &&
		value.ReasoningEffort != "xhigh" && value.ReasoningEffort != "max" &&
		value.ReasoningEffort != "ultra" {
		return errors.New("runtime.settings_applied reasoningEffort 无效")
	}
	appliedTier, ok := codexsettings.AppliedServiceTier(value.ServiceTier)
	if !ok {
		return errors.New("runtime.settings_applied serviceTier 无效")
	}
	value.ServiceTier = appliedTier
	if value.CollaborationMode != "" && value.CollaborationMode != "default" &&
		value.CollaborationMode != "plan" {
		return errors.New("runtime.settings_applied collaborationMode 无效")
	}
	if value.SettingsRevision < 0 {
		return errors.New("runtime.settings_applied settingsRevision 无效")
	}
	_, err := tx.ExecContext(ctx, `UPDATE codex_turn_runs SET
		applied_model = NULLIF($2,''), applied_reasoning_effort = NULLIF($3,''),
		applied_service_tier = NULLIF($4,''), applied_collaboration_mode = NULLIF($5,''),
		applied_settings_revision = $6, settings_applied_at = now()
		WHERE id = $1`, claimed.RunID, value.Model, value.ReasoningEffort,
		value.ServiceTier, value.CollaborationMode, value.SettingsRevision)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE codex_thread_controls SET
		applied_model = NULLIF($2,''), applied_reasoning_effort = NULLIF($3,''),
		applied_service_tier = NULLIF($4,''), applied_collaboration_mode = NULLIF($5,''),
		applied_settings_revision = $6, settings_applied_at = now(), updated_at = now()
		WHERE id = $1 AND (applied_settings_revision IS NULL OR applied_settings_revision <= $6)`,
		claimed.ControlID, value.Model, value.ReasoningEffort, value.ServiceTier,
		value.CollaborationMode, value.SettingsRevision)
	return err
}

func completedUserMessage(payload json.RawMessage) (string, string, bool) {
	var value struct {
		Item struct {
			ID                  string `json:"id"`
			Type                string `json:"type"`
			ClientID            string `json:"clientId"`
			ClientUserMessageID string `json:"clientUserMessageId"`
		} `json:"item"`
	}
	if json.Unmarshal(payload, &value) != nil || value.Item.Type != "userMessage" ||
		value.Item.ID == "" {
		return "", "", false
	}
	clientID := value.Item.ClientID
	if clientID == "" {
		clientID = value.Item.ClientUserMessageID
	}
	return value.Item.ID, clientID, true
}

func recordDesktopUserMessageItem(ctx context.Context, tx *sql.Tx,
	claimed *codexcontrol.ClaimedControl, itemID, clientID string,
) error {
	if claimed.InputSurface != "desktop" {
		return nil
	}
	var intentID uuid.UUID
	query := `UPDATE codex_turn_intents SET
		codex_user_message_item_id = COALESCE(codex_user_message_item_id, $3),
		updated_at = now()
		WHERE control_id = $1 AND input_surface = 'desktop'
			AND desktop_input_projection_key = $2
		RETURNING id`
	err := tx.QueryRowContext(ctx, query, claimed.ControlID, clientID, itemID).Scan(&intentID)
	if errors.Is(err, sql.ErrNoRows) && clientID == "" {
		err = tx.QueryRowContext(ctx, `UPDATE codex_turn_intents SET
			codex_user_message_item_id=$3,updated_at=now()
			WHERE id=(SELECT id FROM codex_turn_intents
				WHERE control_id=$1 AND input_surface='desktop'
					AND confirmed_codex_turn_id=COALESCE(NULLIF($2,''),(
						SELECT confirmed_codex_turn_id FROM codex_turn_runs WHERE id=$4))
					AND codex_user_message_item_id IS NULL
				ORDER BY sequence_no LIMIT 1 FOR UPDATE)
			RETURNING id`, claimed.ControlID, claimed.ConfirmedTurnID, itemID,
			claimed.RunID).Scan(&intentID)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE desktop_thread_requests request SET
		codex_user_message_item_id = COALESCE(request.codex_user_message_item_id, $2),
		updated_at = now()
		FROM codex_turn_intents intent
		WHERE intent.id = $1 AND request.control_id = intent.control_id
			AND request.first_input_projection_key = intent.desktop_input_projection_key`,
		intentID, itemID)
	return err
}

func (s *Server) workerRunComplete(c *gin.Context) {
	var request workerprotocol.CompleteRequest
	runID, worker, ok := requireRunLease(c, &request)
	if !ok {
		return
	}
	if request.IdempotencyKey == "" {
		badRequest(c, errors.New("完成请求缺少幂等键"))
		return
	}
	if finished, err := s.remoteRunAlreadyFinished(c.Request.Context(), runID, worker.ID,
		request.IdempotencyKey, "completed"); err != nil {
		remoteRunError(c, "检查远程任务完成状态失败", err)
		return
	} else if finished {
		c.Status(http.StatusNoContent)
		return
	}
	claimed, err := s.claimedRemoteRun(c.Request.Context(), worker.ID, runID, request.RunLeaseRequest)
	repository := codexcontrol.NewRepository(s.db, s.cfg.LeaseDuration)
	if err == nil {
		var satisfied bool
		satisfied, err = repository.ReplySatisfied(c.Request.Context(), claimed)
		if err == nil && !satisfied {
			err = errors.New("required_reply_missing")
		}
	}
	if err == nil && claimed.SourceType == codexcontrol.SourceWorkspace {
		s.hydrateDesktopConversation(c.Request.Context(), claimed)
		if claimed.DiscordConversationID != uuid.Nil {
			projectionErr := s.projectRemoteDiscordComplete(c.Request.Context(), claimed, request.Result)
			if claimed.InputSurface != "desktop" {
				err = projectionErr
			}
		}
	}
	if err == nil {
		err = repository.Complete(c.Request.Context(), claimed, request.Result)
	}
	if err != nil {
		remoteRunError(c, "完成远程任务失败", err)
		return
	}
	_, _ = s.db.ExecContext(c.Request.Context(), `UPDATE codex_turn_runs
		SET worker_terminal_key = $2 WHERE id = $1`, runID, request.IdempotencyKey)
	c.Status(http.StatusNoContent)
}

func (s *Server) discordProjectionTarget(ctx context.Context,
	claimed *codexcontrol.ClaimedControl,
) (string, string, error) {
	s.hydrateDesktopConversation(ctx, claimed)
	var guildID, threadID string
	err := s.db.QueryRowContext(ctx, `SELECT guild_id, thread_id FROM discord_conversations
		WHERE id = $1`, claimed.DiscordConversationID).Scan(&guildID, &threadID)
	return guildID, threadID, err
}

func (s *Server) hydrateDesktopConversation(ctx context.Context,
	claimed *codexcontrol.ClaimedControl,
) {
	if claimed.InputSurface != "desktop" || claimed.DiscordConversationID != uuid.Nil {
		return
	}
	_ = s.db.QueryRowContext(ctx, `SELECT discord_conversation_id
		FROM codex_thread_controls WHERE id = $1 AND discord_conversation_id IS NOT NULL`,
		claimed.ControlID).Scan(&claimed.DiscordConversationID)
}

func (s *Server) projectRemoteDiscordProgress(ctx context.Context,
	claimed *codexcontrol.ClaimedControl, payload json.RawMessage,
) {
	var progress struct {
		State  string `json:"state"`
		Detail string `json:"detail"`
	}
	if json.Unmarshal(payload, &progress) != nil {
		return
	}
	state := discordintegration.ConversationRunning
	if progress.State == "completed" {
		state = discordintegration.ConversationCompleted
	}
	guildID, threadID, err := s.discordProjectionTarget(ctx, claimed)
	if err == nil {
		anchor := discordProjectionAnchor(claimed)
		_ = discordintegration.ProjectConversationStatus(ctx, s.db, guildID, threadID,
			claimed.DiscordConversationID, anchor, claimed.RunID, state, progress.Detail)
	}
}

func (s *Server) projectRemoteDiscordComplete(ctx context.Context,
	claimed *codexcontrol.ClaimedControl, result codexcontrol.TurnResult,
) error {
	guildID, threadID, err := s.discordProjectionTarget(ctx, claimed)
	if err != nil {
		return err
	}
	anchor := discordProjectionAnchor(claimed)
	if err := discordintegration.ProjectConversationStatus(ctx, s.db, guildID, threadID,
		claimed.DiscordConversationID, anchor, claimed.RunID,
		discordintegration.ConversationCompleted, "本轮处理完成。"); err != nil {
		return err
	}
	if result.FinalAnswer != "" || len(result.AttachmentIDs) == 0 {
		if err := discordintegration.ProjectConversationReply(ctx, s.db, threadID,
			claimed.DiscordConversationID, anchor, claimed.RunID, result.FinalAnswer,
			result.FinalOutputType); err != nil {
			return err
		}
	}
	if err := discordintegration.ProjectConversationImages(ctx, s.db, threadID,
		claimed.DiscordConversationID, anchor, claimed.RunID, result.AttachmentIDs); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE discord_input_messages SET status = 'processed',
		processed_at = now() WHERE turn_intent_id IN (
			SELECT id FROM codex_turn_intents WHERE control_id = $1 AND
				(id = $2 OR (resolved_action = 'steer' AND confirmed_codex_turn_id = $3))
		)`, claimed.ControlID, claimed.ID, result.TurnID)
	return err
}

func discordProjectionAnchor(claimed *codexcontrol.ClaimedControl) string {
	if claimed.ProjectionAnchor != "" {
		return claimed.ProjectionAnchor
	}
	if claimed.DiscordMessageID != "" {
		return claimed.DiscordMessageID
	}
	return "desktop-" + claimed.ID.String()
}

func recordDiscordIntentContributors(ctx context.Context, tx *sql.Tx, runID,
	conversationID, intentID uuid.UUID, turnID string,
) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO discord_turn_contributors
		(run_id, conversation_id, external_turn_id, discord_user_id, first_message_id,
		 github_binding_id, github_user_id, github_login, binding_version)
		SELECT DISTINCT ON (message.discord_user_id) $1, $2, $3,
			message.discord_user_id, message.message_id, message.github_binding_id,
			message.github_user_id, message.github_login, message.binding_version
		FROM discord_input_messages message WHERE message.turn_intent_id = $4
		ORDER BY message.discord_user_id, message.received_at, message.message_id
		ON CONFLICT(run_id, discord_user_id) DO NOTHING`, runID, conversationID, turnID, intentID)
	return err
}

func (s *Server) workerRunFail(c *gin.Context) {
	var request workerprotocol.FailRequest
	runID, worker, ok := requireRunLease(c, &request)
	if !ok {
		return
	}
	if request.IdempotencyKey == "" {
		badRequest(c, errors.New("失败请求缺少幂等键"))
		return
	}
	expectedStatus := "failed"
	if request.Code == "user_interrupt" {
		expectedStatus = "canceled"
	}
	if finished, err := s.remoteRunAlreadyFinished(c.Request.Context(), runID, worker.ID,
		request.IdempotencyKey, expectedStatus); err != nil {
		remoteRunError(c, "检查远程任务失败状态失败", err)
		return
	} else if finished {
		c.Status(http.StatusNoContent)
		return
	}
	claimed, err := s.claimedRemoteRun(c.Request.Context(), worker.ID, runID, request.RunLeaseRequest)
	if err == nil {
		if request.Code == "codex_non_retryable_error" && request.CodexError == nil {
			badRequest(c, errors.New("不可重试 Codex 错误缺少结构化详情"))
			return
		}
		if request.CodexError != nil {
			if request.Code != "codex_non_retryable_error" {
				badRequest(c, errors.New("结构化 Codex 错误只能用于不可重试失败码"))
				return
			}
			if request.CodexError.WillRetry || request.CodexError.Message == "" ||
				request.CodexError.ThreadID == "" || request.CodexError.TurnID == "" {
				badRequest(c, errors.New("不可重试 Codex 错误字段不完整"))
				return
			}
			if request.Message != request.CodexError.Message {
				badRequest(c, errors.New("结构化 Codex 错误消息与终态消息不一致"))
				return
			}
			if claimed.ExternalThreadID != "" && request.CodexError.ThreadID != claimed.ExternalThreadID {
				badRequest(c, errors.New("结构化 Codex 错误 Thread ID 与 Run 不匹配"))
				return
			}
			expectedTurnID := claimed.ConfirmedTurnID
			if expectedTurnID == "" {
				expectedTurnID = claimed.SubmissionID
			}
			if expectedTurnID != "" && request.CodexError.TurnID != expectedTurnID {
				badRequest(c, errors.New("结构化 Codex 错误 Turn ID 与 Run 不匹配"))
				return
			}
		}
		repository := codexcontrol.NewRepository(s.db, s.cfg.LeaseDuration)
		switch request.Code {
		case "user_interrupt":
			err = repository.Cancel(c.Request.Context(), claimed, request.Code, request.Message)
		case "codex_non_retryable_error":
			err = repository.FailWithCodexError(c.Request.Context(), claimed, request.Code,
				emptyMessageError(request.Message), request.CodexError)
		default:
			err = repository.Reconcile(c.Request.Context(), claimed, request.Code,
				emptyMessageError(request.Message))
		}
	}
	if err != nil {
		remoteRunError(c, "提交远程任务失败状态失败", err)
		return
	}
	if claimed.SourceType == codexcontrol.SourceWorkspace {
		guildID, threadID, targetErr := s.discordProjectionTarget(c.Request.Context(), claimed)
		if targetErr == nil {
			state := discordintegration.ConversationFailed
			detail := "本轮处理未完成。"
			if request.Code == "user_interrupt" {
				state = discordintegration.ConversationCanceled
				detail = "本轮已由 Discord 用户主动停止。"
			}
			var errorDetails *discordintegration.ComponentErrorPayload
			if request.CodexError != nil {
				errorDetails = discordintegration.CodexErrorForProjection(request.CodexError)
			}
			_ = discordintegration.ProjectConversationStatus(c.Request.Context(), s.db,
				guildID, threadID, claimed.DiscordConversationID, discordProjectionAnchor(claimed), claimed.RunID,
				state, detail, errorDetails)
		}
	}
	_, _ = s.db.ExecContext(c.Request.Context(), `UPDATE codex_turn_runs
		SET worker_terminal_key = $2 WHERE id = $1`, runID, request.IdempotencyKey)
	c.Status(http.StatusNoContent)
}

func (s *Server) remoteRunAlreadyFinished(ctx context.Context, runID, workerID uuid.UUID,
	key, expectedStatus string,
) (bool, error) {
	var status string
	var storedKey sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT status, worker_terminal_key
		FROM codex_turn_runs WHERE id = $1 AND worker_id = $2`, runID, workerID).
		Scan(&status, &storedKey)
	if err != nil {
		return false, err
	}
	if storedKey.Valid && storedKey.String != key {
		return false, errors.New("run 已使用不同幂等键结束")
	}
	if status == expectedStatus {
		if !storedKey.Valid {
			_, err = s.db.ExecContext(ctx, `UPDATE codex_turn_runs SET worker_terminal_key = $2
				WHERE id = $1 AND worker_terminal_key IS NULL`, runID, key)
		}
		return err == nil, err
	}
	if status == "completed" || status == "failed" || status == "canceled" {
		return false, errors.New("run 已进入不同终态")
	}
	return false, nil
}

func (s *Server) workerSetThread(c *gin.Context) {
	var request workerprotocol.SetThreadRequest
	runID, worker, ok := requireRunLease(c, &request)
	if !ok {
		return
	}
	claimed, err := s.claimedRemoteRun(c.Request.Context(), worker.ID, runID, request.RunLeaseRequest)
	if err == nil {
		err = codexcontrol.NewRepository(s.db, s.cfg.LeaseDuration).SetThread(c.Request.Context(),
			claimed, request.ThreadID)
	}
	if err != nil {
		remoteRunError(c, "保存远程 Codex Thread 失败", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) workerRecordSubmission(c *gin.Context) {
	var request workerprotocol.SubmissionRequest
	runID, worker, ok := requireRunLease(c, &request)
	if !ok {
		return
	}
	claimed, err := s.claimedRemoteRun(c.Request.Context(), worker.ID, runID, request.RunLeaseRequest)
	if err == nil {
		err = codexcontrol.NewRepository(s.db, s.cfg.LeaseDuration).RecordSubmission(
			c.Request.Context(), claimed, request.SubmissionID)
	}
	if err == nil && claimed.SourceType == codexcontrol.SourceWorkspace {
		s.hydrateDesktopConversation(c.Request.Context(), claimed)
		if claimed.DiscordConversationID != uuid.Nil {
			err = discordintegration.ExpireConversationPlanCards(c.Request.Context(), s.db,
				claimed.DiscordConversationID, claimed.RunID)
		}
	}
	if err == nil && claimed.SourceType == codexcontrol.SourceWorkspace &&
		claimed.DiscordConversationID != uuid.Nil {
		tx, txErr := s.db.BeginTx(c.Request.Context(), nil)
		if txErr == nil {
			txErr = recordDiscordIntentContributors(c.Request.Context(), tx, claimed.RunID,
				claimed.DiscordConversationID, claimed.ID, request.SubmissionID)
			if txErr == nil {
				txErr = tx.Commit()
			} else {
				_ = tx.Rollback()
			}
		}
		err = txErr
	}
	if err != nil {
		remoteRunError(c, "记录远程 Codex 提交失败", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) workerConfirmTurn(c *gin.Context) {
	var request workerprotocol.ConfirmTurnRequest
	runID, worker, ok := requireRunLease(c, &request)
	if !ok {
		return
	}
	claimed, err := s.claimedRemoteRun(c.Request.Context(), worker.ID, runID, request.RunLeaseRequest)
	if err == nil {
		err = codexcontrol.NewRepository(s.db, s.cfg.LeaseDuration).ConfirmTurn(
			c.Request.Context(), claimed, request.TurnID)
	}
	if err != nil {
		remoteRunError(c, "确认远程 Codex Turn 失败", err)
		return
	}
	c.Status(http.StatusNoContent)
}
