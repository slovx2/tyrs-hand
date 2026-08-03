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
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/slovx2/tyrs-hand/internal/workerregistry"
)

const workerContextKey = "worker"

func (s *Server) registerWorkerRoutes(router *gin.Engine) {
	group := router.Group("/worker/v1")
	group.Use(s.requireWorkerIP())
	group.Use(func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
	})
	group.POST("/enroll", s.enrollWorker)
	authorized := group.Group("")
	authorized.Use(s.requireWorker())
	authorized.GET("/blobs/:id", s.workerBlob)
	authorized.POST("/blobs/:id", s.workerBlob)
	s.registerWorkerOperationRoutes(authorized)
}

func (s *Server) registerWorkerOperationRoutes(group *gin.RouterGroup) {
	group.POST("/heartbeat", s.workerHeartbeat)
	group.POST("/claims", s.workerClaim)
	group.GET("/ssh-configuration", s.workerSSHConfiguration)
	group.GET("/workspace", s.workerWorkspace)
	group.POST("/workspace/projects/snapshot", s.workerWorkspaceProjectSnapshot)
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
	group.POST("/runs/:id/workspace-project-state", s.workerWorkspaceProjectState)
	group.POST("/runs/:id/workspace-state", s.workerWorkspaceState)
	group.POST("/runs/:id/tools/call", s.workerToolCall)
	group.POST("/runs/:id/git-credential", s.workerGitCredential)
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
	configuration, err := s.ssh.WorkerConfiguration(c.Request.Context(), currentWorker(c).ID)
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

func (s *Server) enrollWorker(c *gin.Context) {
	var request workerprotocol.EnrollRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	worker, credential, err := s.workers.Enroll(c.Request.Context(), request.Token)
	if err != nil {
		problem(c, http.StatusUnauthorized, "注册Worker失败", err)
		return
	}
	c.JSON(http.StatusOK, workerprotocol.EnrollResponse{WorkerID: worker.ID,
		Credential: credential, ProtocolVersion: workerregistry.ProtocolVersion})
}

func (s *Server) requireWorker() gin.HandlerFunc {
	return func(c *gin.Context) {
		value := c.GetHeader("Authorization")
		token, ok := strings.CutPrefix(value, "Bearer ")
		if !ok || strings.TrimSpace(token) == "" {
			problem(c, http.StatusUnauthorized, "缺少Worker凭据", nil)
			c.Abort()
			return
		}
		worker, err := s.workers.Authenticate(c.Request.Context(), strings.TrimSpace(token))
		if err != nil {
			problem(c, http.StatusUnauthorized, "Worker认证失败", err)
			c.Abort()
			return
		}
		c.Set(workerContextKey, worker)
		c.Next()
	}
}

func currentWorker(c *gin.Context) workerregistry.Worker {
	value, _ := c.Get(workerContextKey)
	worker, _ := value.(workerregistry.Worker)
	return worker
}

func (s *Server) workerHeartbeat(c *gin.Context) {
	var request workerprotocol.HeartbeatRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	worker := currentWorker(c)
	if err := s.workers.Heartbeat(c.Request.Context(), worker.ID, request.WorkerVersion,
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
	worker := currentWorker(c)
	if worker.Status == "incompatible" {
		problem(c, http.StatusConflict, "Worker 协议版本不兼容，禁止领取任务", nil)
		return
	}
	if !workerregistry.HasRole(worker, request.Role) {
		problem(c, http.StatusForbidden, "节点未授权该 Worker 角色", nil)
		return
	}
	source := ""
	switch request.Role {
	case "github":
		source = codexcontrol.SourceGitHub
	case "discord":
		source = codexcontrol.SourceWorkspace
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
			WHERE worker_id = $1 AND active_slot = 1`, worker.ID).
			Scan(&active); err != nil {
			problem(c, http.StatusInternalServerError, "读取节点运行槽位失败", err)
			return
		}
		if active >= worker.MaxConcurrentJobs {
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
		claimed, err := repository.ClaimWorker(c.Request.Context(), worker.ID.String(), source, worker.ID)
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

func (s *Server) claimedRemoteRun(ctx context.Context, workerID, runID uuid.UUID,
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
		i.workspace_project_id::text,
		COALESCE(i.discord_message_id,''), i.agent_profile_id, i.sequence_no,
		i.target_intent_id::text, COALESCE(i.projection_anchor,''),
		i.message_edit_revision, COALESCE(i.replacement_phase,''),
		i.status = 'reconciling' OR i.codex_submission_id IS NOT NULL,
		COALESCE(i.codex_submission_id,''), COALESCE(i.confirmed_codex_turn_id,''),
		COALESCE(c.external_thread_id,'')
		FROM codex_turn_runs r JOIN codex_turn_intents i ON i.id = r.primary_intent_id
		JOIN codex_thread_controls c ON c.id = r.control_id
		WHERE r.id = $1 AND r.worker_id = $2`, runID, workerID).Scan(
		&claimed.ControlID, &claimed.ID, &claimed.RunID, &claimed.LeaseEpoch, &source,
		&claimed.InputSurface, &claimed.Operation, &claimed.Attempt, &claimed.MaxAttempts,
		&conversationID, &sessionID, &workItemID, &repositoryID, &projectID,
		&claimed.DiscordMessageID, &claimed.AgentProfileID, &claimed.Sequence,
		&targetIntentID, &claimed.ProjectionAnchor, &claimed.MessageEditRevision,
		&claimed.ReplacementPhase,
		&claimed.Recovering, &claimed.SubmissionID, &claimed.ConfirmedTurnID,
		&claimed.ExternalThreadID)
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

func requireRunLease(c *gin.Context, target any) (uuid.UUID, workerregistry.Worker, bool) {
	id, ok := parseRunID(c)
	if !ok {
		return uuid.Nil, workerregistry.Worker{}, false
	}
	if err := c.ShouldBindJSON(target); err != nil {
		badRequest(c, err)
		return uuid.Nil, workerregistry.Worker{}, false
	}
	return id, currentWorker(c), true
}

func emptyMessageError(message string) error {
	if strings.TrimSpace(message) == "" {
		return errors.New("宿主 Worker 没有提供失败原因")
	}
	return fmt.Errorf("%s", message)
}
