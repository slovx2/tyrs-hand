package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
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
		node.ID).Scan(&claimed.ControlID, &claimed.DiscordConversationID,
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
	claimed.Attempt, claimed.MaxAttempts = 1, max(1, s.cfg.CodexReconcileMaxAttempts)
	claimed.LeaseToken, claimed.LeaseEpoch = leaseToken, oldLeaseEpoch+1
	claimed.LeaseExpiresAt = time.Now().Add(s.cfg.LeaseDuration)
	idempotencyKey := "desktop-turn:" + request.EnvironmentID.String() + ":" + request.RequestKey
	_, err = tx.ExecContext(c.Request.Context(), `INSERT INTO codex_turn_intents
		(id, control_id, sequence_no, operation, behavior, source_type, input_surface,
		 discord_conversation_id, repository_id, agent_profile_id, idempotency_key,
		 instruction, prepared_input, allowed_tools, dangerous_actions, priority,
		 actor_login, actor_permission, actor_participant_id, actor_display_name,
		 reply_policy, reply_status, status, attempt_count, max_attempts, dispatched_at)
		VALUES ($1,$2,$3,'turn_input','start_when_idle','discord_conversation','desktop',$4,$5,$6,$7,
			$8,$9,$10,$11,100,'codex-desktop','owner',NULLIF($12::text,'')::uuid,$13,
			'silent','skipped','dispatching',1,$14,now())`,
		claimed.ID, claimed.ControlID, claimed.Sequence, claimed.DiscordConversationID,
		claimed.RepositoryID, claimed.AgentProfileID, idempotencyKey, instruction, request.Params,
		allowedJSON, dangerousJSON, nilUUIDString(claimed.ActorParticipantID),
		claimed.ActorDisplayName, claimed.MaxAttempts)
	if err != nil {
		problem(c, http.StatusConflict, "Desktop Turn 已提交或发生并发冲突", err)
		return
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
		if item.Type == "text" && strings.TrimSpace(item.Text) != "" {
			parts = append(parts, strings.TrimSpace(item.Text))
		}
	}
	return value.ThreadID, strings.Join(parts, "\n\n"), nil
}
