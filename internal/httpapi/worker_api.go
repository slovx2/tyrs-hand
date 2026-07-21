package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/executionnode"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

const workerNodeContextKey = "execution_node"

func (s *Server) registerWorkerRoutes(router *gin.Engine) {
	group := router.Group("/worker/v1")
	group.Use(s.requireWorkerIP())
	group.Use(func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
	})
	group.POST("/enroll", s.enrollWorkerNode)
	authorized := group.Group("")
	authorized.Use(s.requireWorkerNode())
	authorized.POST("/heartbeat", s.workerHeartbeat)
	authorized.POST("/claims", s.workerClaim)
	authorized.POST("/runs/:id/heartbeat", s.workerRunHeartbeat)
	authorized.POST("/runs/:id/commands/ack", s.workerCommandAck)
	authorized.POST("/runs/:id/events", s.workerRunEvents)
	authorized.POST("/runs/:id/complete", s.workerRunComplete)
	authorized.POST("/runs/:id/fail", s.workerRunFail)
	authorized.POST("/runs/:id/runtime-credential", s.workerRuntimeCredential)
	authorized.POST("/runs/:id/thread", s.workerSetThread)
	authorized.POST("/runs/:id/submission", s.workerRecordSubmission)
	authorized.POST("/runs/:id/confirm", s.workerConfirmTurn)
	authorized.POST("/runs/:id/development-state", s.workerDevelopmentState)
	authorized.POST("/runs/:id/workspace-state", s.workerWorkspaceState)
	authorized.POST("/runs/:id/discord-title", s.workerDiscordTitle)
	authorized.POST("/runs/:id/tools/call", s.workerToolCall)
	authorized.POST("/runs/:id/git-credential", s.workerGitCredential)
	authorized.GET("/runs/:id/attachments/:attachmentId", s.workerDownloadAttachment)
	authorized.POST("/development-operations/:id/heartbeat", s.workerDevelopmentOperationHeartbeat)
	authorized.POST("/development-operations/:id/complete", s.workerCompleteDevelopmentOperation)
	authorized.POST("/development-operations/:id/fail", s.workerFailDevelopmentOperation)
}

func (s *Server) enrollWorkerNode(c *gin.Context) {
	var request workerprotocol.EnrollRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	node, credential, err := s.nodes.Enroll(c.Request.Context(), request.Token)
	if err != nil {
		problem(c, http.StatusUnauthorized, "注册执行节点失败", err)
		return
	}
	c.JSON(http.StatusOK, workerprotocol.EnrollResponse{NodeID: node.ID,
		Credential: credential, ProtocolVersion: executionnode.ProtocolVersion})
}

func (s *Server) requireWorkerNode() gin.HandlerFunc {
	return func(c *gin.Context) {
		value := c.GetHeader("Authorization")
		token, ok := strings.CutPrefix(value, "Bearer ")
		if !ok || strings.TrimSpace(token) == "" {
			problem(c, http.StatusUnauthorized, "缺少执行节点凭据", nil)
			c.Abort()
			return
		}
		node, err := s.nodes.Authenticate(c.Request.Context(), strings.TrimSpace(token))
		if err != nil {
			problem(c, http.StatusUnauthorized, "执行节点认证失败", err)
			c.Abort()
			return
		}
		c.Set(workerNodeContextKey, node)
		c.Next()
	}
}

func workerNode(c *gin.Context) executionnode.Node {
	value, _ := c.Get(workerNodeContextKey)
	node, _ := value.(executionnode.Node)
	return node
}

func (s *Server) workerHeartbeat(c *gin.Context) {
	var request workerprotocol.HeartbeatRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	node := workerNode(c)
	if err := s.nodes.Heartbeat(c.Request.Context(), node.ID, request.WorkerVersion,
		request.ProtocolVersion, request.Metadata); err != nil {
		problem(c, http.StatusInternalServerError, "更新节点心跳失败", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) workerClaim(c *gin.Context) {
	var request workerprotocol.ClaimRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	node := workerNode(c)
	if node.Status == "incompatible" {
		problem(c, http.StatusConflict, "Worker 协议版本不兼容，禁止领取任务", nil)
		return
	}
	if !executionnode.HasRole(node, request.Role) {
		problem(c, http.StatusForbidden, "节点未授权该 Worker 角色", nil)
		return
	}
	source := ""
	switch request.Role {
	case "github":
		source = codexcontrol.SourceGitHub
	case "discord":
		source = codexcontrol.SourceDiscord
	default:
		badRequest(c, errors.New("role 必须是 github 或 discord"))
		return
	}
	deadline := time.Now()
	if request.Wait {
		deadline = deadline.Add(25 * time.Second)
	}
	repository := codexcontrol.NewRepository(s.db, s.cfg.LeaseDuration,
		s.cfg.CodexMaxSteersPerTurn, s.cfg.CodexReconcileMaxAttempts)
	for {
		var active int
		if err := s.db.QueryRowContext(c.Request.Context(), `SELECT
			(SELECT count(*) FROM codex_turn_runs
				WHERE execution_node_id = $1 AND active_slot = 1) +
			(SELECT count(*) FROM discord_development_operations
				WHERE execution_node_id = $1 AND status = 'running'
				AND lease_expires_at >= now())`, node.ID).
			Scan(&active); err != nil {
			problem(c, http.StatusInternalServerError, "读取节点运行槽位失败", err)
			return
		}
		if active >= node.MaxConcurrentJobs {
			if !request.Wait || !time.Now().Before(deadline) {
				c.JSON(http.StatusOK, workerprotocol.ClaimResponse{})
				return
			}
			select {
			case <-c.Request.Context().Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		if request.Role == "discord" {
			operation, err := s.claimDevelopmentOperation(c.Request.Context(), node.ID,
				request.WorkerID)
			if err != nil {
				problem(c, http.StatusInternalServerError, "领取开发环境 Operation 失败", err)
				return
			}
			if operation != nil {
				c.JSON(http.StatusOK, workerprotocol.ClaimResponse{
					DevelopmentOperation: operation,
				})
				return
			}
		}
		claimed, err := repository.ClaimNode(c.Request.Context(), request.WorkerID, source, node.ID)
		if err != nil {
			problem(c, http.StatusInternalServerError, "领取远程任务失败", err)
			return
		}
		if claimed != nil {
			snapshot, err := s.loadWorkerSnapshot(c.Request.Context(), claimed)
			if err != nil {
				_ = repository.Reconcile(c.Request.Context(), claimed, "snapshot_error", err)
				problem(c, http.StatusInternalServerError, "生成任务快照失败", err)
				return
			}
			c.JSON(http.StatusOK, workerprotocol.ClaimResponse{Task: &workerprotocol.Task{
				Claimed: *claimed, Snapshot: snapshot,
			}})
			return
		}
		if !time.Now().Before(deadline) {
			c.JSON(http.StatusOK, workerprotocol.ClaimResponse{})
			return
		}
		select {
		case <-c.Request.Context().Done():
			return
		case <-time.After(time.Second):
		}
	}
}

func (s *Server) claimedRemoteRun(ctx context.Context, nodeID, runID uuid.UUID,
	lease workerprotocol.RunLeaseRequest,
) (*codexcontrol.ClaimedControl, error) {
	var claimed codexcontrol.ClaimedControl
	var source string
	var conversationID, workItemID, repositoryID sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT r.control_id, r.primary_intent_id, r.id,
		r.lease_epoch, i.source_type, i.attempt_count, i.max_attempts,
		i.discord_conversation_id::text, i.work_item_id::text, i.repository_id::text,
		COALESCE(i.discord_message_id,''), i.agent_profile_id, i.sequence_no,
		i.status = 'reconciling' OR i.codex_submission_id IS NOT NULL,
		COALESCE(i.codex_submission_id,''), COALESCE(i.confirmed_codex_turn_id,''),
		COALESCE(c.external_thread_id,''), COALESCE(c.codex_home_key,''),
		COALESCE(c.provider_signature,'')
		FROM codex_turn_runs r JOIN codex_turn_intents i ON i.id = r.primary_intent_id
		JOIN codex_thread_controls c ON c.id = r.control_id
		WHERE r.id = $1 AND r.execution_node_id = $2`, runID, nodeID).Scan(
		&claimed.ControlID, &claimed.ID, &claimed.RunID, &claimed.LeaseEpoch, &source,
		&claimed.Attempt, &claimed.MaxAttempts, &conversationID, &workItemID, &repositoryID,
		&claimed.DiscordMessageID, &claimed.AgentProfileID, &claimed.Sequence,
		&claimed.Recovering, &claimed.SubmissionID, &claimed.ConfirmedTurnID,
		&claimed.ExternalThreadID, &claimed.CodexHomeKey, &claimed.ProviderSignature)
	if err != nil {
		return nil, err
	}
	if claimed.LeaseEpoch != lease.LeaseEpoch {
		return nil, codexcontrol.ErrLeaseLost
	}
	claimed.LeaseToken, claimed.SourceType = lease.LeaseToken, source
	if conversationID.Valid {
		claimed.DiscordConversationID, err = uuid.Parse(conversationID.String)
	}
	if err == nil && workItemID.Valid {
		claimed.WorkItemID, err = uuid.Parse(workItemID.String)
	}
	if err == nil && repositoryID.Valid {
		claimed.RepositoryID, err = uuid.Parse(repositoryID.String)
	}
	if err != nil {
		return nil, err
	}
	return &claimed, nil
}

func parseRunID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return uuid.Nil, false
	}
	return id, true
}

func remoteRunError(c *gin.Context, action string, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, sql.ErrNoRows) {
		status = http.StatusNotFound
	} else if errors.Is(err, codexcontrol.ErrLeaseLost) {
		status = http.StatusConflict
	}
	problem(c, status, action, err)
}

func requireRunLease(c *gin.Context, target any) (uuid.UUID, executionnode.Node, bool) {
	id, ok := parseRunID(c)
	if !ok {
		return uuid.Nil, executionnode.Node{}, false
	}
	if err := c.ShouldBindJSON(target); err != nil {
		badRequest(c, err)
		return uuid.Nil, executionnode.Node{}, false
	}
	return id, workerNode(c), true
}

func emptyMessageError(message string) error {
	if strings.TrimSpace(message) == "" {
		return errors.New("远程 Worker 没有提供失败原因")
	}
	return fmt.Errorf("%s", message)
}
