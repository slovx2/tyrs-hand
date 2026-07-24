package httpapi

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

func (s *Server) workerDevelopmentState(c *gin.Context) {
	var request workerprotocol.DevelopmentState
	runID, node, ok := requireRunLease(c, &request)
	if !ok {
		return
	}
	claimed, err := s.claimedRemoteRun(c.Request.Context(), node.ID, runID,
		request.RunLeaseRequest)
	if err != nil {
		remoteRunError(c, "校验开发环境状态请求失败", err)
		return
	}
	if claimed.SourceType != codexcontrol.SourceDiscord || request.EnvironmentID == uuid.Nil ||
		request.ForumID == uuid.Nil {
		badRequest(c, errors.New("开发环境状态只允许用于 Discord Run"))
		return
	}
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "保存开发环境状态失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var matches bool
	err = tx.QueryRowContext(c.Request.Context(), `SELECT EXISTS(
		SELECT 1 FROM discord_conversations c JOIN discord_forums f ON f.id = c.forum_id
		WHERE c.id = $1 AND f.id = $2 AND f.development_environment_id = $3)`,
		claimed.DiscordConversationID, request.ForumID, request.EnvironmentID).Scan(&matches)
	if err != nil || !matches {
		problem(c, http.StatusForbidden, "开发环境不属于当前 Run", err)
		return
	}
	status := request.EnvironmentStatus
	if request.Error != "" {
		status = "error"
	}
	_, err = tx.ExecContext(c.Request.Context(), `UPDATE discord_development_environments SET
		status = $2, image_ref = NULLIF($3,''), image_id = NULLIF($4,''),
		container_id = NULLIF($5,''), runtime_user = NULLIF($6,''), runtime_uid = NULLIF($7,0),
		runtime_gid = NULLIF($8,0), runtime_home = NULLIF($9,''),
		error = NULLIF($10,''),
		last_used_at = now(), updated_at = now() WHERE id = $1`, request.EnvironmentID,
		status, request.ImageRef, request.ImageID, request.ContainerID, request.RuntimeUser,
		request.RuntimeUID, request.RuntimeGID, request.RuntimeHome, request.Error)
	if err == nil {
		workspaceStatus := request.WorkspaceStatus
		if request.Error != "" {
			workspaceStatus = "error"
		}
		_, err = tx.ExecContext(c.Request.Context(), `UPDATE discord_forum_workspaces SET
			status = $2, head_sha = NULLIF($3,''), dirty = $4, error = NULLIF($5,''),
			last_used_at = now(), updated_at = now() WHERE forum_id = $1`, request.ForumID,
			workspaceStatus, request.WorkspaceHeadSHA, request.WorkspaceDirty, request.Error)
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "保存开发环境状态失败", err)
		return
	}
	if err := tx.Commit(); err != nil {
		problem(c, http.StatusInternalServerError, "提交开发环境状态失败", err)
		return
	}
	c.Status(http.StatusNoContent)
}
