package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/scheduledtasks"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

func (s *Server) workerToolCall(c *gin.Context) {
	var request workerprotocol.ToolCallRequest
	runID, worker, ok := requireWorkerRun(c, &request)
	if !ok {
		return
	}
	claimed, err := s.claimedRemoteRun(c.Request.Context(), worker.ID, runID)
	if err != nil {
		remoteRunError(c, "校验 Dynamic Tool Run 失败", err)
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
	problem(c, http.StatusGone, "GitHub 功能已停用", nil)
}

func (s *Server) workerGitCredential(c *gin.Context) {
	var request workerprotocol.GitCredentialRequest
	runID, worker, ok := requireWorkerRun(c, &request)
	if !ok {
		return
	}
	if _, err := s.claimedRemoteRun(c.Request.Context(), worker.ID, runID); err != nil {
		remoteRunError(c, "校验 Git 凭据 Run 失败", err)
		return
	}
	problem(c, http.StatusGone, "GitHub 功能已停用", nil)
}
