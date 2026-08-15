package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/scheduledtasks"
	"github.com/slovx2/tyrs-hand/internal/security"
	toolservice "github.com/slovx2/tyrs-hand/internal/tools"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

func (s *Server) workerCapability(ctx context.Context, runID, workerID uuid.UUID,
	capability string,
) error {
	if capability == "" {
		return errors.New("任务 Capability 不能为空")
	}
	var matches bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM codex_turn_runs
		WHERE id = $1 AND worker_id = $2 AND capability_hash = $3 AND active_slot = 1)`,
		runID, workerID, security.Digest(capability)).Scan(&matches)
	if err != nil {
		return err
	}
	if !matches {
		return errors.New("任务 Capability 与当前节点 Run 不匹配")
	}
	return nil
}

func (s *Server) workerToolCall(c *gin.Context) {
	var request workerprotocol.ToolCallRequest
	runID, worker, ok := requireRunLease(c, &request)
	if !ok {
		return
	}
	claimed, err := s.claimedRemoteRun(c.Request.Context(), worker.ID, runID,
		request.RunLeaseRequest)
	if err != nil {
		remoteRunError(c, "校验 Dynamic Tool Run 失败", err)
		return
	}
	if err := s.workerCapability(c.Request.Context(), runID, worker.ID, request.Capability); err != nil {
		problem(c, http.StatusForbidden, "校验 Dynamic Tool Capability 失败", err)
		return
	}
	namespace := ""
	if request.Request.Namespace != nil {
		namespace = *request.Request.Namespace
	}
	if namespace == "tyrs_hand" && request.Request.Tool == "automation_update" {
		if claimed.SourceType != codexcontrol.SourceWorkspace || claimed.SessionID == uuid.Nil {
			problem(c, http.StatusForbidden, "定时任务工具只允许 Workspace Session 使用", nil)
			return
		}
		result, callErr := scheduledtasks.NewService(s.db, s.cfg.LeaseDuration,
			s.cfg.CodexMaxSteersPerTurn, s.cfg.CodexReconcileMaxAttempts).Call(
			c.Request.Context(), scheduledtasks.ToolContext{RunID: runID,
				IntentID: claimed.ID, SessionID: claimed.SessionID, ProjectID: claimed.ProjectID,
				AgentProfileID: claimed.AgentProfileID, ExternalThread: claimed.ExternalThreadID,
				ThreadID: request.Request.ThreadID, TurnID: request.Request.TurnID,
				CallID: request.Request.CallID}, request.Request.Arguments)
		if callErr != nil {
			problem(c, http.StatusForbidden, "定时任务工具调用失败", callErr)
			return
		}
		c.JSON(http.StatusOK, result)
		return
	}
	_, app, _, configured := s.github.Current()
	if !configured {
		problem(c, http.StatusServiceUnavailable, "GitHub App 尚未配置", nil)
		return
	}
	result, err := toolservice.NewService(s.db, app, s.catalog).Call(c.Request.Context(),
		toolservice.CallRequest{Capability: request.Capability,
			ThreadID: request.Request.ThreadID, TurnID: request.Request.TurnID,
			CallID: request.Request.CallID, Namespace: namespace, Tool: request.Request.Tool,
			Arguments: request.Request.Arguments})
	if err != nil {
		problem(c, http.StatusForbidden, "工具调用失败", err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (s *Server) workerGitCredential(c *gin.Context) {
	var request workerprotocol.GitCredentialRequest
	runID, worker, ok := requireRunLease(c, &request)
	if !ok {
		return
	}
	if _, err := s.claimedRemoteRun(c.Request.Context(), worker.ID, runID,
		request.RunLeaseRequest); err != nil {
		remoteRunError(c, "校验 Git 凭据 Run 失败", err)
		return
	}
	if err := s.workerCapability(c.Request.Context(), runID, worker.ID, request.Capability); err != nil {
		problem(c, http.StatusForbidden, "校验 Git 凭据 Capability 失败", err)
		return
	}
	_, app, _, configured := s.github.Current()
	if !configured {
		problem(c, http.StatusServiceUnavailable, "GitHub App 尚未配置", nil)
		return
	}
	token, err := toolservice.NewService(s.db, app, s.catalog).GitCredential(
		c.Request.Context(), request.Capability, request.Purpose, request.TurnID)
	if err != nil {
		problem(c, http.StatusForbidden, "Git 凭据请求失败", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"token": token, "expiresInSeconds": 3600})
}
