package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

func (s *Server) workerRunHeartbeat(c *gin.Context) {
	var request workerprotocol.RunLeaseRequest
	runID, node, ok := requireRunLease(c, &request)
	if !ok {
		return
	}
	claimed, err := s.claimedRemoteRun(c.Request.Context(), node.ID, runID, request)
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
			ExternalThreadID: claimed.ExternalThreadID, CodexHomeKey: claimed.CodexHomeKey}})
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
		if claimed.SourceType == codexcontrol.SourceDiscord && messageID != "" {
			copyClaimed := *claimed
			copyClaimed.ID, copyClaimed.DiscordMessageID = command.ID, messageID
			command.Discord, err = s.loadDiscordWorkerSnapshot(ctx, &copyClaimed)
			if err != nil {
				return nil, err
			}
		}
		commands = append(commands, command)
	}
	return commands, rows.Err()
}

func (s *Server) workerCommandAck(c *gin.Context) {
	var request workerprotocol.CommandAckRequest
	runID, node, ok := requireRunLease(c, &request)
	if !ok {
		return
	}
	claimed, err := s.claimedRemoteRun(c.Request.Context(), node.ID, runID,
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
	var operation, status string
	err = tx.QueryRowContext(c.Request.Context(), `SELECT operation, status
		FROM codex_turn_intents WHERE id = $1 AND control_id = $2 FOR UPDATE`,
		request.CommandID, claimed.ControlID).Scan(&operation, &status)
	if err != nil {
		remoteRunError(c, "Run 指令不存在", err)
		return
	}
	if status == "completed" || (status == "running" && request.Action == "steer") {
		c.Status(http.StatusNoContent)
		return
	}
	if operation != request.Action && (operation != "turn_input" || request.Action != "steer") {
		badRequest(c, errors.New("run 指令确认与原操作不匹配"))
		return
	}
	if request.Action == "interrupt" {
		_, err = tx.ExecContext(c.Request.Context(), `UPDATE codex_turn_intents SET
			status = 'completed', resolved_action = 'interrupt', confirmed_codex_turn_id = $2,
			finished_at = now(), updated_at = now() WHERE id = $1`, request.CommandID,
			request.TurnID)
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
	runID, node, ok := requireRunLease(c, &request)
	if !ok {
		return
	}
	claimed, err := s.claimedRemoteRun(c.Request.Context(), node.ID, runID, request.RunLeaseRequest)
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
		FROM codex_turn_runs WHERE id = $1 AND execution_node_id = $2 FOR UPDATE`,
		runID, node.ID).Scan(&lastSequence); err != nil {
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
		_, err = tx.ExecContext(c.Request.Context(), `INSERT INTO agent_events
			(control_id, intent_id, run_id, event_type, external_event_id, payload)
			VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT(run_id, external_event_id)
			WHERE run_id IS NOT NULL AND external_event_id IS NOT NULL DO NOTHING`,
			claimed.ControlID, claimed.ID, claimed.RunID, event.Type,
			fmt.Sprintf("worker:%d", event.Sequence), event.Payload)
		if err != nil {
			problem(c, http.StatusInternalServerError, "记录远程事件失败", err)
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
			}
		}
		lastSequence = event.Sequence
	}
	if _, err := tx.ExecContext(c.Request.Context(), `UPDATE codex_turn_runs
		SET worker_event_sequence = $2 WHERE id = $1`, runID, lastSequence); err != nil {
		problem(c, http.StatusInternalServerError, "更新远程事件序列失败", err)
		return
	}
	if err := tx.Commit(); err != nil {
		problem(c, http.StatusInternalServerError, "提交远程事件失败", err)
		return
	}
	if claimed.SourceType == codexcontrol.SourceDiscord {
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
			codex_user_message_item_id = COALESCE(codex_user_message_item_id, $2),
			updated_at = now()
			WHERE id = $1 AND input_surface = 'desktop' RETURNING id`,
			claimed.ID, itemID).Scan(&intentID)
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
	runID, node, ok := requireRunLease(c, &request)
	if !ok {
		return
	}
	if request.IdempotencyKey == "" {
		badRequest(c, errors.New("完成请求缺少幂等键"))
		return
	}
	if finished, err := s.remoteRunAlreadyFinished(c.Request.Context(), runID, node.ID,
		request.IdempotencyKey, "completed"); err != nil {
		remoteRunError(c, "检查远程任务完成状态失败", err)
		return
	} else if finished {
		c.Status(http.StatusNoContent)
		return
	}
	claimed, err := s.claimedRemoteRun(c.Request.Context(), node.ID, runID, request.RunLeaseRequest)
	repository := codexcontrol.NewRepository(s.db, s.cfg.LeaseDuration)
	if err == nil {
		var satisfied bool
		satisfied, err = repository.ReplySatisfied(c.Request.Context(), claimed)
		if err == nil && !satisfied {
			err = errors.New("required_reply_missing")
		}
	}
	if err == nil && claimed.SourceType == codexcontrol.SourceDiscord {
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
	if err := discordintegration.ProjectConversationReply(ctx, s.db, threadID,
		claimed.DiscordConversationID, anchor, result.FinalAnswer); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE discord_input_messages SET status = 'processed',
		processed_at = now() WHERE message_id = $1`, claimed.DiscordMessageID)
	return err
}

func discordProjectionAnchor(claimed *codexcontrol.ClaimedControl) string {
	if claimed.DiscordMessageID != "" {
		return claimed.DiscordMessageID
	}
	return "desktop-" + claimed.ID.String()
}

func (s *Server) workerRunFail(c *gin.Context) {
	var request workerprotocol.FailRequest
	runID, node, ok := requireRunLease(c, &request)
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
	if finished, err := s.remoteRunAlreadyFinished(c.Request.Context(), runID, node.ID,
		request.IdempotencyKey, expectedStatus); err != nil {
		remoteRunError(c, "检查远程任务失败状态失败", err)
		return
	} else if finished {
		c.Status(http.StatusNoContent)
		return
	}
	claimed, err := s.claimedRemoteRun(c.Request.Context(), node.ID, runID, request.RunLeaseRequest)
	if err == nil {
		repository := codexcontrol.NewRepository(s.db, s.cfg.LeaseDuration)
		if request.Code == "user_interrupt" {
			err = repository.Cancel(c.Request.Context(), claimed, request.Code, request.Message)
		} else {
			err = repository.Reconcile(c.Request.Context(), claimed, request.Code,
				emptyMessageError(request.Message))
		}
	}
	if err != nil {
		remoteRunError(c, "提交远程任务失败状态失败", err)
		return
	}
	if claimed.SourceType == codexcontrol.SourceDiscord {
		guildID, threadID, targetErr := s.discordProjectionTarget(c.Request.Context(), claimed)
		if targetErr == nil {
			state := discordintegration.ConversationFailed
			detail := "本轮处理未完成。"
			if request.Code == "user_interrupt" {
				state = discordintegration.ConversationCanceled
				detail = "本轮已由 Discord 用户主动停止。"
			}
			_ = discordintegration.ProjectConversationStatus(c.Request.Context(), s.db,
				guildID, threadID, claimed.DiscordConversationID, discordProjectionAnchor(claimed), claimed.RunID,
				state, detail)
		}
	}
	_, _ = s.db.ExecContext(c.Request.Context(), `UPDATE codex_turn_runs
		SET worker_terminal_key = $2 WHERE id = $1`, runID, request.IdempotencyKey)
	c.Status(http.StatusNoContent)
}

func (s *Server) remoteRunAlreadyFinished(ctx context.Context, runID, nodeID uuid.UUID,
	key, expectedStatus string,
) (bool, error) {
	var status string
	var storedKey sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT status, worker_terminal_key
		FROM codex_turn_runs WHERE id = $1 AND execution_node_id = $2`, runID, nodeID).
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

func (s *Server) workerRuntimeCredential(c *gin.Context) {
	var request workerprotocol.RunLeaseRequest
	runID, node, ok := requireRunLease(c, &request)
	if !ok {
		return
	}
	if _, err := s.claimedRemoteRun(c.Request.Context(), node.ID, runID, request); err != nil {
		remoteRunError(c, "校验运行凭据请求失败", err)
		return
	}
	credential, err := s.codexRuntimeCredential(c.Request.Context())
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Provider 凭据失败", err)
		return
	}
	c.JSON(http.StatusOK, credential)
}

func (s *Server) workerSetThread(c *gin.Context) {
	var request workerprotocol.SetThreadRequest
	runID, node, ok := requireRunLease(c, &request)
	if !ok {
		return
	}
	claimed, err := s.claimedRemoteRun(c.Request.Context(), node.ID, runID, request.RunLeaseRequest)
	if err == nil {
		err = codexcontrol.NewRepository(s.db, s.cfg.LeaseDuration).SetThread(c.Request.Context(),
			claimed, request.ThreadID, request.CodexHome)
	}
	if err != nil {
		remoteRunError(c, "保存远程 Codex Thread 失败", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) workerRecordSubmission(c *gin.Context) {
	var request workerprotocol.SubmissionRequest
	runID, node, ok := requireRunLease(c, &request)
	if !ok {
		return
	}
	claimed, err := s.claimedRemoteRun(c.Request.Context(), node.ID, runID, request.RunLeaseRequest)
	if err == nil {
		err = codexcontrol.NewRepository(s.db, s.cfg.LeaseDuration).RecordSubmission(
			c.Request.Context(), claimed, request.SubmissionID)
	}
	if err != nil {
		remoteRunError(c, "记录远程 Codex 提交失败", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) workerConfirmTurn(c *gin.Context) {
	var request workerprotocol.ConfirmTurnRequest
	runID, node, ok := requireRunLease(c, &request)
	if !ok {
		return
	}
	claimed, err := s.claimedRemoteRun(c.Request.Context(), node.ID, runID, request.RunLeaseRequest)
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
