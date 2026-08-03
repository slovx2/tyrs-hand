package httpapi

import (
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
	if err := s.discord.DeleteWorkspaceProjectForumAccess(
		c, projectID, forumID, c.Param("memberId")); err != nil {
		badRequest(c, err)
		return
	}
	c.Status(http.StatusNoContent)
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
