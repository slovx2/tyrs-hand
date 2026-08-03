package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	group := router.Group("/worker/v2")
	group.Use(s.requireWorkerIP())
	group.Use(func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
	})
	group.POST("/enroll", s.enrollWorkerNode)
	authorized := group.Group("")
	authorized.Use(s.requireWorkerNode())
	authorized.POST("/sync", s.workerSync)
	authorized.POST("/rpc", s.workerRPC)
	authorized.GET("/blobs/:id", s.workerBlob)
	authorized.POST("/blobs/:id", s.workerBlob)
}

func (s *Server) registerWorkerOperationRoutes(group *gin.RouterGroup) {
	group.POST("/heartbeat", s.workerHeartbeat)
	group.POST("/claims", s.workerClaim)
	group.GET("/ssh-configuration", s.workerSSHConfiguration)
	group.GET("/development-environments", s.workerDevelopmentEnvironments)
	group.POST("/development-environments/:id/daemon-state", s.workerEnvironmentDaemonState)
	group.POST("/development-environments/:id/projects/snapshot", s.workerDevelopmentProjectSnapshot)
	group.POST("/development-environments/:id/interactive/interrupted", s.workerInterruptEnvironmentInteractive)
	group.POST("/desktop-thread-requests", s.workerPrepareDesktopThread)
	group.GET("/desktop-thread-requests/:id", s.workerDesktopThreadState)
	group.POST("/desktop-thread-requests/:id/complete", s.workerCompleteDesktopThread)
	group.POST("/desktop-thread-requests/:id/fail", s.workerFailDesktopThread)
	group.POST("/thread-metadata-events", s.workerRecordThreadMetadata)
	group.GET("/thread-name-updates", s.workerPendingThreadNames)
	group.POST("/thread-name-updates/:id/ack", s.workerAckThreadName)
	group.POST("/thread-lifecycle-requests/desktop", s.workerPrepareDesktopThreadLifecycle)
	group.GET("/thread-lifecycle-requests", s.workerPendingThreadLifecycles)
	group.GET("/thread-lifecycle-requests/:id", s.workerThreadLifecycleState)
	group.POST("/thread-lifecycle-requests/:id/complete", s.workerCompleteThreadLifecycle)
	group.POST("/desktop-turns", s.workerPrepareDesktopTurn)
	group.POST("/desktop-turns/preflight", s.workerPreflightDesktopTurn)
	group.GET("/desktop-turns/:id/images/target", s.workerDesktopImageTarget)
	group.POST("/desktop-turns/:id/images/:ordinal/fail", s.workerFailDesktopImage)
	group.POST("/desktop-rollbacks", s.workerPrepareDesktopRollback)
	group.POST("/desktop-rollbacks/:id/complete", s.workerCompleteDesktopRollback)
	group.POST("/desktop-steers", s.workerRecordDesktopSteer)
	group.POST("/runs/:id/interactive", s.workerRegisterInteractive)
	group.GET("/interactive/:id", s.workerInteractiveState)
	group.POST("/interactive/answer", s.workerAnswerInteractive)
	group.POST("/runs/:id/heartbeat", s.workerRunHeartbeat)
	group.POST("/runs/:id/commands/ack", s.workerCommandAck)
	group.POST("/runs/:id/events", s.workerRunEvents)
	group.POST("/runs/:id/complete", s.workerRunComplete)
	group.POST("/runs/:id/fail", s.workerRunFail)
	group.POST("/runs/:id/thread", s.workerSetThread)
	group.POST("/runs/:id/submission", s.workerRecordSubmission)
	group.POST("/runs/:id/confirm", s.workerConfirmTurn)
	group.POST("/runs/:id/development-state", s.workerDevelopmentState)
	group.POST("/runs/:id/workspace-state", s.workerWorkspaceState)
	group.POST("/runs/:id/tools/call", s.workerToolCall)
	group.POST("/runs/:id/git-credential", s.workerGitCredential)
}

func (s *Server) workerSync(c *gin.Context) {
	var request workerprotocol.RequestEnvelope
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	if request.Operation != "worker.heartbeat" && request.Operation != "worker.claim" {
		badRequest(c, errors.New("sync 仅承载心跳和任务领取"))
		return
	}
	s.dispatchWorkerOperation(c, request)
}

func (s *Server) workerRPC(c *gin.Context) {
	var request workerprotocol.RequestEnvelope
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	if request.Operation == "worker.heartbeat" || request.Operation == "worker.claim" {
		badRequest(c, errors.New("该操作不允许通过 worker rpc 调用"))
		return
	}
	s.dispatchWorkerOperation(c, request)
}

func (s *Server) dispatchWorkerOperation(c *gin.Context,
	request workerprotocol.RequestEnvelope,
) {
	if _, err := uuid.Parse(request.RequestID); err != nil || request.Sequence == 0 {
		badRequest(c, errors.New("worker 请求缺少稳定 requestId 或 sequence"))
		return
	}
	method, path, err := workerprotocol.ResolveOperationRoute(request.Operation,
		request.Parameters)
	if err != nil {
		badRequest(c, err)
		return
	}
	engine := gin.New()
	node := workerNode(c)
	engine.Use(func(inner *gin.Context) {
		inner.Set(workerNodeContextKey, node)
		inner.Next()
	})
	s.registerWorkerOperationRoutes(engine.Group("/worker/v1"))
	body := bytes.NewReader(request.Payload)
	innerRequest := httptest.NewRequest(method, path, body).
		WithContext(c.Request.Context())
	innerRequest.Header.Set("Content-Type", "application/json")
	if etag := strings.TrimSpace(request.Parameters["ifNoneMatch"]); etag != "" {
		innerRequest.Header.Set("If-None-Match", etag)
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, innerRequest)
	for name, values := range recorder.Header() {
		for _, value := range values {
			c.Writer.Header().Add(name, value)
		}
	}
	c.Data(recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.Bytes())
}

func (s *Server) workerBlob(c *gin.Context) {
	switch c.Request.Method {
	case http.MethodGet:
		runID := strings.TrimSpace(c.Query("runId"))
		if _, err := uuid.Parse(runID); err != nil {
			badRequest(c, errors.New("blob 下载缺少有效 runId"))
			return
		}
		if _, err := uuid.Parse(c.Param("id")); err != nil {
			badRequest(c, errors.New("blob id 无效"))
			return
		}
		c.Params = gin.Params{{Key: "id", Value: runID},
			{Key: "attachmentId", Value: c.Param("id")}}
		s.workerDownloadAttachment(c)
	case http.MethodPost:
		ordinal := strings.TrimSpace(c.Query("ordinal"))
		if _, err := uuid.Parse(c.Param("id")); err != nil {
			badRequest(c, errors.New("blob intent id 无效"))
			return
		}
		if ordinal == "" {
			badRequest(c, errors.New("blob 上传缺少 ordinal"))
			return
		}
		c.Params = gin.Params{{Key: "id", Value: c.Param("id")},
			{Key: "ordinal", Value: ordinal}}
		s.workerUploadDesktopImage(c)
	default:
		c.Status(http.StatusMethodNotAllowed)
	}
}

func (s *Server) workerSSHConfiguration(c *gin.Context) {
	configuration, err := s.ssh.NodeConfiguration(c.Request.Context(), workerNode(c).ID)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取节点 SSH 配置失败", err)
		return
	}
	etag := `"` + configuration.Revision + `"`
	c.Header("ETag", etag)
	if c.GetHeader("If-None-Match") == etag {
		c.Status(http.StatusNotModified)
		return
	}
	c.JSON(http.StatusOK, configuration)
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
		source = codexcontrol.SourceDevelopment
	case "all":
		source = ""
	default:
		badRequest(c, errors.New("role 必须是 all、github 或 discord"))
		return
	}
	deadline := time.Now()
	if request.Wait {
		deadline = deadline.Add(10 * time.Second)
	}
	repository := codexcontrol.NewRepository(s.db, s.cfg.LeaseDuration,
		s.cfg.CodexMaxSteersPerTurn, s.cfg.CodexReconcileMaxAttempts)
	for {
		var active int
		if err := s.db.QueryRowContext(c.Request.Context(), `SELECT count(*)
			FROM codex_turn_runs
			WHERE execution_node_id = $1 AND active_slot = 1`, node.ID).
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
	var conversationID, sessionID, workItemID, repositoryID, projectID sql.NullString
	var targetIntentID sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT r.control_id, r.primary_intent_id, r.id,
		r.lease_epoch, i.source_type, COALESCE(i.input_surface,''), i.operation,
		i.attempt_count, i.max_attempts,
		i.discord_conversation_id::text, i.session_id::text, i.work_item_id::text, i.repository_id::text,
		i.development_project_id::text,
		COALESCE(i.discord_message_id,''), i.agent_profile_id, i.sequence_no,
		i.target_intent_id::text, COALESCE(i.projection_anchor,''),
		i.message_edit_revision, COALESCE(i.replacement_phase,''),
		i.status = 'reconciling' OR i.codex_submission_id IS NOT NULL,
		COALESCE(i.codex_submission_id,''), COALESCE(i.confirmed_codex_turn_id,''),
		COALESCE(c.external_thread_id,''), COALESCE(c.codex_home_key,'')
		FROM codex_turn_runs r JOIN codex_turn_intents i ON i.id = r.primary_intent_id
		JOIN codex_thread_controls c ON c.id = r.control_id
		WHERE r.id = $1 AND r.execution_node_id = $2`, runID, nodeID).Scan(
		&claimed.ControlID, &claimed.ID, &claimed.RunID, &claimed.LeaseEpoch, &source,
		&claimed.InputSurface, &claimed.Operation, &claimed.Attempt, &claimed.MaxAttempts,
		&conversationID, &sessionID, &workItemID, &repositoryID, &projectID,
		&claimed.DiscordMessageID, &claimed.AgentProfileID, &claimed.Sequence,
		&targetIntentID, &claimed.ProjectionAnchor, &claimed.MessageEditRevision,
		&claimed.ReplacementPhase,
		&claimed.Recovering, &claimed.SubmissionID, &claimed.ConfirmedTurnID,
		&claimed.ExternalThreadID, &claimed.CodexHomeKey)
	if err != nil {
		return nil, err
	}
	if claimed.LeaseEpoch != lease.LeaseEpoch {
		return nil, codexcontrol.ErrLeaseLost
	}
	claimed.LeaseToken, claimed.SourceType = lease.LeaseToken, source
	if targetIntentID.Valid {
		claimed.TargetIntentID, err = uuid.Parse(targetIntentID.String)
	}
	if conversationID.Valid {
		claimed.DiscordConversationID, err = uuid.Parse(conversationID.String)
	}
	if err == nil && sessionID.Valid {
		claimed.SessionID, err = uuid.Parse(sessionID.String)
	}
	if err == nil && workItemID.Valid {
		claimed.WorkItemID, err = uuid.Parse(workItemID.String)
	}
	if err == nil && repositoryID.Valid {
		claimed.RepositoryID, err = uuid.Parse(repositoryID.String)
	}
	if err == nil && projectID.Valid {
		claimed.ProjectID, err = uuid.Parse(projectID.String)
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
