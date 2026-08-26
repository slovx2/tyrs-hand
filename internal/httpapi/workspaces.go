package httpapi

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/auth"
)

func (s *Server) listWorkspaces(c *gin.Context) {
	workspaces, err := s.discord.Workspaces(c)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Workspace 失败", err)
		return
	}
	session := c.MustGet("session").(auth.Session)
	if session.Role != "admin" {
		filtered := workspaces[:0]
		for _, workspace := range workspaces {
			if workspace.WorkerID == nil {
				continue
			}
			allowed, accessErr := s.workerAllowed(c.Request.Context(), session, *workspace.WorkerID)
			if accessErr != nil {
				problem(c, http.StatusInternalServerError, "检查 Workspace 权限失败", accessErr)
				return
			}
			if allowed {
				filtered = append(filtered, workspace)
			}
		}
		workspaces = filtered
	}
	c.JSON(http.StatusOK, gin.H{"items": workspaces})
}

func (s *Server) createWorkspace(c *gin.Context) {
	var input struct {
		OwnerDiscordUserID string    `json:"ownerDiscordUserId" binding:"required"`
		WorkerID           uuid.UUID `json:"workerId" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		badRequest(c, err)
		return
	}
	if !s.requireWorkerAccess(c, input.WorkerID) {
		return
	}
	workspaceID, err := s.discord.CreateWorkspace(c, input.OwnerDiscordUserID, input.WorkerID)
	if err != nil {
		problem(c, http.StatusConflict, "创建 Workspace 失败", err)
		return
	}
	s.audit(c, "workspace.create", "workspace",
		workspaceID.String(), map[string]any{"ownerDiscordUserId": input.OwnerDiscordUserID,
			"workerId": input.WorkerID})
	c.JSON(http.StatusCreated, gin.H{"id": workspaceID})
}

func (s *Server) createWorkspaceProjectForum(c *gin.Context) {
	projectID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var input struct {
		Mode    string     `json:"mode" binding:"required"`
		ForumID *uuid.UUID `json:"forumId"`
		Name    string     `json:"name"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		badRequest(c, err)
		return
	}
	if !s.requireProjectWorkerAccess(c, projectID) {
		return
	}
	switch input.Mode {
	case "restore":
		if input.ForumID == nil {
			badRequest(c, errors.New("恢复 Forum 必须指定 forumId"))
			return
		}
		if err := s.discord.RestoreWorkspaceForum(c, projectID, *input.ForumID); err != nil {
			problem(c, http.StatusConflict, "恢复 Workspace Forum 失败", err)
			return
		}
		s.audit(c, "workspace_forum.restore", "discord_forum",
			input.ForumID.String(), map[string]any{"projectId": projectID})
		c.Status(http.StatusAccepted)
	case "new":
		guild, err := s.discordGuild(c)
		if err != nil {
			problem(c, http.StatusBadGateway, "读取 Discord Guild 失败", err)
			return
		}
		plan, err := s.discord.WorkspaceProjectForumPlan(c, guild, projectID, input.Name)
		if err != nil {
			problem(c, http.StatusConflict, "创建 Workspace Forum 预检失败", err)
			return
		}
		administratorID := c.MustGet("session").(auth.Session).AdministratorID
		operationID, err := s.discord.CreateInitialization(c, administratorID, plan, "")
		if err != nil {
			problem(c, http.StatusConflict, "创建 Workspace Forum 失败", err)
			return
		}
		s.audit(c, "workspace_forum.create", "workspace_project", projectID.String(),
			map[string]any{"operationId": operationID})
		c.JSON(http.StatusAccepted, gin.H{"id": operationID})
	default:
		badRequest(c, errors.New("mode 必须是 new 或 restore"))
	}
}

func (s *Server) disableWorkspaceForum(c *gin.Context) {
	s.setWorkspaceForumEnabled(c, false)
}

func (s *Server) enableWorkspaceForum(c *gin.Context) {
	s.setWorkspaceForumEnabled(c, true)
}

func (s *Server) setWorkspaceForumEnabled(c *gin.Context, enabled bool) {
	forumID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	if !s.requireForumWorkerAccess(c, forumID) {
		return
	}
	var err error
	if enabled {
		err = s.discord.EnableWorkspaceForum(c, forumID)
	} else {
		err = s.discord.DisableWorkspaceForum(c, forumID)
	}
	if err != nil {
		problem(c, http.StatusConflict, "更新开发 Forum 配对状态失败", err)
		return
	}
	action := "disable"
	if enabled {
		action = "enable"
	}
	s.audit(c, "workspace_forum."+action, "discord_forum", forumID.String(), nil)
	c.Status(http.StatusAccepted)
}

func (s *Server) putWorkspaceProjectForumCollaborator(c *gin.Context) {
	projectID, forumID, ok := parseProjectForumParams(c)
	if !ok {
		return
	}
	if !s.requireProjectWorkerAccess(c, projectID) {
		return
	}
	var input struct {
		AccessLevel string `json:"accessLevel" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		badRequest(c, err)
		return
	}
	administratorID := c.MustGet("session").(auth.Session).AdministratorID
	if err := s.discord.SetWorkspaceProjectForumAccess(c, projectID, forumID,
		c.Param("memberId"), input.AccessLevel, administratorID); err != nil {
		badRequest(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) deleteWorkspaceProjectForumCollaborator(c *gin.Context) {
	projectID, forumID, ok := parseProjectForumParams(c)
	if !ok {
		return
	}
	if !s.requireProjectWorkerAccess(c, projectID) {
		return
	}
	if err := s.discord.DeleteWorkspaceProjectForumAccess(
		c, projectID, forumID, c.Param("memberId")); err != nil {
		badRequest(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) requireProjectWorkerAccess(c *gin.Context, projectID uuid.UUID) bool {
	var workerID uuid.UUID
	err := s.db.QueryRowContext(c.Request.Context(), `SELECT worker_id FROM workspace_projects p JOIN worker_workspaces w ON w.id=p.workspace_id WHERE p.id=$1`, projectID).Scan(&workerID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			problem(c, http.StatusNotFound, "项目不存在", err)
		} else {
			problem(c, http.StatusInternalServerError, "读取项目 Worker 失败", err)
		}
		return false
	}
	return s.requireWorkerAccess(c, workerID)
}

func (s *Server) requireForumWorkerAccess(c *gin.Context, forumID uuid.UUID) bool {
	var workerID uuid.UUID
	err := s.db.QueryRowContext(c.Request.Context(), `SELECT w.worker_id FROM discord_forums f JOIN worker_workspaces w ON w.id=f.workspace_id WHERE f.id=$1`, forumID).Scan(&workerID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			problem(c, http.StatusNotFound, "Forum 不存在", err)
		} else {
			problem(c, http.StatusInternalServerError, "读取 Forum Worker 失败", err)
		}
		return false
	}
	return s.requireWorkerAccess(c, workerID)
}

func parseProjectForumParams(c *gin.Context) (uuid.UUID, uuid.UUID, bool) {
	projectID, ok := parseUUIDParam(c, "id")
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	forumID, ok := parseUUIDParam(c, "forumId")
	return projectID, forumID, ok
}

func parseUUIDParam(c *gin.Context, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param(name))
	if err != nil {
		badRequest(c, err)
		return uuid.Nil, false
	}
	return id, true
}
