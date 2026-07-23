package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/slovx2/tyrs-hand/internal/participantidentity"
	"github.com/slovx2/tyrs-hand/internal/security"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

func (s *Server) workerPrepareDesktopTurn(c *gin.Context) {
	var request workerprotocol.DesktopTurnPrepareRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	threadID, instruction, err := desktopTurnInput(request.Params)
	if err != nil || request.EnvironmentID == uuid.Nil || strings.TrimSpace(request.WorkerID) == "" ||
		!validDesktopRequestKey(request.RequestKey) {
		badRequest(c, errors.New("desktop turn 参数无效"))
		return
	}
	projectionKey := desktopInputProjectionKey(request.Params, request.RequestKey)
	leaseToken, err := security.RandomToken(32)
	if err != nil {
		problem(c, http.StatusInternalServerError, "创建 Desktop Turn Lease 失败", err)
		return
	}
	capability, err := security.RandomToken(32)
	if err != nil {
		problem(c, http.StatusInternalServerError, "创建 Desktop Turn Capability 失败", err)
		return
	}
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "创建 Desktop Turn 失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	node := workerNode(c)
	var active int
	if err := tx.QueryRowContext(c.Request.Context(), `SELECT
		(SELECT count(*) FROM codex_turn_runs WHERE execution_node_id = n.id AND active_slot = 1) +
		(SELECT count(*) FROM discord_development_operations WHERE execution_node_id = n.id
			AND status = 'running' AND lease_expires_at >= now())
		FROM execution_nodes n WHERE n.id = $1 FOR UPDATE`, node.ID).Scan(&active); err != nil {
		problem(c, http.StatusInternalServerError, "读取 Desktop Turn 调度槽位失败", err)
		return
	}
	if active >= node.MaxConcurrentJobs {
		problem(c, http.StatusTooManyRequests, "当前执行节点没有可用的 Turn 槽位", nil)
		return
	}
	var claimed codexcontrol.ClaimedControl
	var controlStatus string
	var allowedJSON, dangerousJSON []byte
	var nextSequence int64
	var oldLeaseEpoch int64
	var actorGuildID, actorUserID, actorDisplayName string
	var conversationID sql.NullString
	err = tx.QueryRowContext(c.Request.Context(), `SELECT ct.id, ct.discord_conversation_id,
		ct.repository_id, ct.agent_profile_id, ct.status, ct.next_sequence_no,
		ct.lease_epoch, COALESCE(ct.external_thread_id,''), COALESCE(ct.codex_home_key,''),
		COALESCE(ct.provider_signature,''), p.allowed_tools, '[]'::jsonb,
		e.guild_id, COALESCE(e.ssh_discord_user_id, ''),
		COALESCE(NULLIF(m.display_name, ''), m.username, '')
		FROM codex_thread_controls ct JOIN agent_profiles p ON p.id = ct.agent_profile_id
		JOIN discord_development_environments e ON e.id = ct.development_environment_id
		LEFT JOIN discord_members m ON m.guild_id = e.guild_id
			AND m.discord_user_id = e.ssh_discord_user_id
		WHERE ct.external_thread_id = $1 AND ct.development_environment_id = $2
		AND ct.execution_node_id = $3 FOR UPDATE OF ct`, threadID, request.EnvironmentID,
		node.ID).Scan(&claimed.ControlID, &conversationID,
		&claimed.RepositoryID, &claimed.AgentProfileID, &controlStatus, &nextSequence,
		&oldLeaseEpoch, &claimed.ExternalThreadID,
		&claimed.CodexHomeKey, &claimed.ProviderSignature, &allowedJSON, &dangerousJSON,
		&actorGuildID, &actorUserID, &actorDisplayName)
	if errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusForbidden, "Desktop Turn 的 Thread 未绑定到当前环境", err)
		return
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Desktop Turn Control 失败", err)
		return
	}
	claimed.DiscordConversationID = parseOptionalUUID(conversationID)
	if controlStatus != "idle" {
		problem(c, http.StatusConflict, "该 Thread 已有活动 Turn", nil)
		return
	}
	_ = json.Unmarshal(allowedJSON, &claimed.AllowedTools)
	_ = json.Unmarshal(dangerousJSON, &claimed.DangerousActions)
	claimed.ID, claimed.RunID = uuid.New(), uuid.New()
	claimed.Sequence, claimed.Operation, claimed.Behavior = nextSequence, "turn_input", "start_when_idle"
	claimed.SourceType, claimed.InputSurface = codexcontrol.SourceDiscord, "desktop"
	claimed.Status, claimed.Instruction = codexcontrol.IntentDispatching, instruction
	claimed.ActorLogin, claimed.ActorPermission, claimed.ReplyPolicy = "codex-desktop", "owner", "silent"
	if actorUserID != "" {
		claimed.ActorParticipantID = participantidentity.ID(actorGuildID, actorUserID)
		claimed.ActorDisplayName = actorDisplayName
	}
	if claimed.DiscordConversationID == uuid.Nil {
		if err := s.queueFirstDesktopInput(c.Request.Context(), tx, claimed.ControlID,
			projectionKey, request.Params, instruction); err != nil {
			problem(c, http.StatusInternalServerError, "排队 Desktop Starter Message 失败", err)
			return
		}
	}
	claimed.Attempt, claimed.MaxAttempts = 1, max(1, s.cfg.CodexReconcileMaxAttempts)
	claimed.LeaseToken, claimed.LeaseEpoch = leaseToken, oldLeaseEpoch+1
	claimed.LeaseExpiresAt = time.Now().Add(s.cfg.LeaseDuration)
	idempotencyKey := "desktop-turn:" + request.EnvironmentID.String() + ":" + request.RequestKey
	_, err = tx.ExecContext(c.Request.Context(), `INSERT INTO codex_turn_intents
			(id, control_id, sequence_no, operation, behavior, source_type, input_surface,
			 discord_conversation_id, repository_id, agent_profile_id, idempotency_key,
			 instruction, prepared_input, allowed_tools, dangerous_actions, priority,
			 actor_login, actor_permission, actor_participant_id, actor_display_name,
			 reply_policy, reply_status, status, attempt_count, max_attempts, dispatched_at,
			 desktop_input_projection_key, desktop_input_projection_status)
			VALUES ($1,$2,$3,'turn_input','start_when_idle','discord_conversation','desktop',
				NULLIF($4::text,'')::uuid,$5,$6,$7,
				$8,$9,$10,$11,100,'codex-desktop','owner',NULLIF($12::text,'')::uuid,$13,
				'silent','skipped','dispatching',1,$14,now(),$15,'pending')`,
		claimed.ID, claimed.ControlID, claimed.Sequence, nilUUIDString(claimed.DiscordConversationID),
		claimed.RepositoryID, claimed.AgentProfileID, idempotencyKey, instruction, request.Params,
		allowedJSON, dangerousJSON, nilUUIDString(claimed.ActorParticipantID),
		claimed.ActorDisplayName, claimed.MaxAttempts, projectionKey)
	if err != nil {
		problem(c, http.StatusConflict, "Desktop Turn 已提交或发生并发冲突", err)
		return
	}
	if claimed.DiscordConversationID != uuid.Nil {
		if err := enqueueDesktopInputProjection(c.Request.Context(), tx,
			claimed.DiscordConversationID, projectionKey, claimed.ActorDisplayName,
			instruction); err != nil {
			problem(c, http.StatusInternalServerError, "投影 Desktop 用户消息失败", err)
			return
		}
		if _, err := tx.ExecContext(c.Request.Context(), `UPDATE codex_turn_intents SET
			desktop_input_projection_status = 'projected', updated_at = now()
			WHERE id = $1`, claimed.ID); err != nil {
			problem(c, http.StatusInternalServerError, "更新 Desktop 用户消息投影状态失败", err)
			return
		}
	}
	_, err = tx.ExecContext(c.Request.Context(), `UPDATE codex_thread_controls SET
		status = 'dispatching', active_intent_id = $2, worker_id = $3, lease_token = $4,
		lease_epoch = $5, lease_expires_at = now() + $6::interval, heartbeat_at = now(),
		next_sequence_no = next_sequence_no + 1, updated_at = now() WHERE id = $1`,
		claimed.ControlID, claimed.ID, request.WorkerID, security.Digest(leaseToken),
		claimed.LeaseEpoch, s.cfg.LeaseDuration.String())
	if err == nil {
		_, err = tx.ExecContext(c.Request.Context(), `INSERT INTO codex_turn_runs
			(id, control_id, primary_intent_id, attempt, worker_id, lease_epoch, capability_hash,
			 active_slot, max_append_count, execution_node_id)
			VALUES ($1,$2,$3,1,$4,$5,$6,1,$7,$8)`, claimed.RunID, claimed.ControlID,
			claimed.ID, request.WorkerID, claimed.LeaseEpoch, security.Digest(capability),
			max(1, s.cfg.CodexMaxSteersPerTurn), node.ID)
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "持久化 Desktop Turn 失败", err)
		return
	}
	if err := tx.Commit(); err != nil {
		problem(c, http.StatusInternalServerError, "提交 Desktop Turn 失败", err)
		return
	}
	claimed.Capability = capability
	snapshot, err := s.loadWorkerSnapshot(c.Request.Context(), &claimed)
	if err != nil {
		_ = codexcontrol.NewRepository(s.db, s.cfg.LeaseDuration).Reconcile(
			c.Request.Context(), &claimed, "snapshot_error", err)
		problem(c, http.StatusInternalServerError, "生成 Desktop Turn 快照失败", err)
		return
	}
	c.JSON(http.StatusOK, workerprotocol.Task{Claimed: claimed, Snapshot: snapshot})
}

func desktopInputProjectionKey(params json.RawMessage, fallback string) string {
	var value struct {
		ClientUserMessageID string `json:"clientUserMessageId"`
	}
	if json.Unmarshal(params, &value) == nil {
		key := strings.TrimSpace(value.ClientUserMessageID)
		if key != "" && len(key) <= 256 {
			return key
		}
	}
	return fallback
}

func desktopTurnInput(params json.RawMessage) (string, string, error) {
	var value struct {
		ThreadID string `json:"threadId"`
		Input    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"input"`
	}
	if json.Unmarshal(params, &value) != nil || strings.TrimSpace(value.ThreadID) == "" {
		return "", "", errors.New("desktop turn 缺少 threadId")
	}
	parts := make([]string, 0, len(value.Input))
	for _, item := range value.Input {
		text := desktopProjectionText(item.Text)
		if item.Type == "text" && text != "" {
			parts = append(parts, text)
		}
	}
	return value.ThreadID, strings.Join(parts, "\n\n"), nil
}

func desktopProjectionText(value string) string {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "<codex_delegation>") ||
		!strings.HasSuffix(value, "</codex_delegation>") {
		return value
	}
	var envelope struct {
		XMLName        xml.Name `xml:"codex_delegation"`
		SourceThreadID string   `xml:"source_thread_id"`
		Input          string   `xml:"input"`
	}
	if err := xml.Unmarshal([]byte(value), &envelope); err != nil ||
		envelope.XMLName.Local != "codex_delegation" ||
		strings.TrimSpace(envelope.SourceThreadID) == "" {
		return value
	}
	return strings.TrimSpace(envelope.Input)
}

func (s *Server) queueFirstDesktopInput(ctx context.Context, tx *sql.Tx, controlID uuid.UUID,
	projectionKey string, params json.RawMessage, instruction string,
) error {
	var requestID uuid.UUID
	var status, desiredName, desiredSource string
	var firstProjectionKey, firstInputText, firstTitle, firstActorID, firstActorName string
	var target desktopThreadTarget
	err := tx.QueryRowContext(ctx, `SELECT r.id, r.status, f.id, resource.discord_id,
		repo.owner || '/' || repo.name, fw.relative_path,
		COALESCE(environment.ssh_discord_user_id, ''),
		COALESCE(NULLIF(member.display_name, ''), member.username, ''),
		COALESCE(control.desired_thread_name,''), COALESCE(control.desired_thread_name_source,''),
		COALESCE(r.first_input_projection_key,''), COALESCE(r.first_input_text,''),
		COALESCE(r.preview_title,''), COALESCE(r.first_input_actor_discord_user_id,''),
		COALESCE(r.first_input_actor_display_name,'')
		FROM desktop_thread_requests r
		JOIN codex_thread_controls control ON control.id = r.control_id
		JOIN discord_forums f ON f.id = r.forum_id
		JOIN discord_resources resource ON resource.id = f.resource_id
		JOIN repositories repo ON repo.id = f.repository_id
		JOIN discord_forum_workspaces fw ON fw.forum_id = f.id
		JOIN discord_development_environments environment ON environment.id = r.environment_id
	LEFT JOIN discord_members member ON member.guild_id = environment.guild_id
			AND member.discord_user_id = environment.ssh_discord_user_id
		WHERE r.control_id = $1 FOR UPDATE OF r`, controlID).Scan(&requestID, &status,
		&target.forumID, &target.forumDiscord, &target.repository, &target.workspacePath,
		&target.actorID, &target.actorName, &desiredName, &desiredSource, &firstProjectionKey,
		&firstInputText, &firstTitle, &firstActorID, &firstActorName)
	if err != nil {
		return err
	}
	if status == "post_pending" || status == "completed" {
		return nil
	}
	if status != "waiting_for_input" && status != "post_failed" {
		return fmt.Errorf("desktop thread 状态 %q 不能创建首条消息", status)
	}
	if status == "post_failed" && firstProjectionKey != "" {
		target.actorID, target.actorName = firstActorID, firstActorName
		_, err = tx.ExecContext(ctx, `UPDATE desktop_thread_requests SET
			status = 'post_pending', error = NULL, updated_at = now() WHERE id = $1`,
			requestID)
		if err != nil {
			return err
		}
		return enqueueDesktopThreadPost(ctx, tx, requestID, target, firstTitle, firstInputText)
	}
	title := normalizeDesktopTitle(instruction)
	if desiredSource == "codex" && desiredName != "" {
		title = desiredName
	}
	_, err = tx.ExecContext(ctx, `UPDATE desktop_thread_requests SET status = 'post_pending',
		first_input_projection_key = $2, first_input = $3, first_input_text = $4,
		preview_title = $5, first_input_actor_discord_user_id = NULLIF($6,''),
		first_input_actor_display_name = NULLIF($7,''), error = NULL, updated_at = now()
		WHERE id = $1`, requestID, projectionKey, params, instruction, title,
		target.actorID, target.actorName)
	if err != nil {
		return err
	}
	return enqueueDesktopThreadPost(ctx, tx, requestID, target, title, instruction)
}

func enqueueDesktopInputProjection(ctx context.Context, tx *sql.Tx, conversationID uuid.UUID,
	projectionKey, displayName, input string,
) error {
	var threadID string
	if err := tx.QueryRowContext(ctx, `SELECT thread_id FROM discord_conversations
		WHERE id = $1`, conversationID).Scan(&threadID); err != nil {
		return err
	}
	return discordintegration.EnqueueDesktopInputPages(ctx, tx, threadID, conversationID,
		projectionKey, displayName, desktopProjectionText(input), 0)
}
