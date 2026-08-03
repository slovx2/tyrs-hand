package httpapi

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

func (s *Server) workerWorkspaceProjectState(c *gin.Context) {
	var request workerprotocol.WorkspaceProjectState
	runID, worker, ok := requireRunLease(c, &request)
	if !ok {
		return
	}
	claimed, err := s.claimedRemoteRun(c.Request.Context(), worker.ID, runID,
		request.RunLeaseRequest)
	if err != nil {
		remoteRunError(c, "校验Workspace状态请求失败", err)
		return
	}
	if claimed.SourceType != codexcontrol.SourceWorkspace || request.WorkspaceID == uuid.Nil ||
		request.ProjectID == uuid.Nil {
		badRequest(c, errors.New("workspace 状态只允许用于 Workspace Session Run"))
		return
	}
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "保存Workspace状态失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var matches bool
	err = tx.QueryRowContext(c.Request.Context(), `SELECT EXISTS(
		SELECT 1 FROM workspace_sessions session
		WHERE session.id=$1 AND session.workspace_id=$2
			AND session.workspace_project_id=$3)`, claimed.SessionID,
		request.WorkspaceID, request.ProjectID).Scan(&matches)
	if err != nil || !matches {
		problem(c, http.StatusForbidden, "Workspace不属于当前 Run", err)
		return
	}
	_, err = tx.ExecContext(c.Request.Context(), `UPDATE workspace_projects project SET
		head_sha=CASE WHEN project.project_kind='git'
			THEN COALESCE(NULLIF($2,''), project.head_sha) ELSE NULL END,
		dirty=CASE WHEN project.project_kind='git' THEN $3 ELSE false END,
		scan_error=NULLIF($4,''), last_seen_at=now(), updated_at=now()
		WHERE project.id=$1`, request.ProjectID, request.WorkspaceHeadSHA,
		request.WorkspaceDirty, request.Error)
	if err != nil {
		problem(c, http.StatusInternalServerError, "保存Workspace状态失败", err)
		return
	}
	if err := tx.Commit(); err != nil {
		problem(c, http.StatusInternalServerError, "提交Workspace状态失败", err)
		return
	}
	c.Status(http.StatusNoContent)
}
