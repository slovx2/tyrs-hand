package httpapi

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/auth"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
)

func (s *Server) listDevelopmentEnvironments(c *gin.Context) {
	environments, err := s.discord.DevelopmentEnvironments(c)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取开发环境失败", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": environments})
}

func (s *Server) createDevelopmentEnvironment(c *gin.Context) {
	var input struct {
		OwnerDiscordUserID string `json:"ownerDiscordUserId" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		badRequest(c, err)
		return
	}
	environmentID, err := s.discord.CreateDevelopmentEnvironment(c, input.OwnerDiscordUserID)
	if err != nil {
		problem(c, http.StatusConflict, "创建开发环境失败", err)
		return
	}
	s.audit(c, "development_environment.create", "development_environment",
		environmentID.String(), map[string]any{"ownerDiscordUserId": input.OwnerDiscordUserID})
	c.JSON(http.StatusCreated, gin.H{"id": environmentID})
}

func (s *Server) putDevelopmentEnvironmentSSH(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var input discordintegration.DevelopmentEnvironmentSSHInput
	if err := c.ShouldBindJSON(&input); err != nil {
		badRequest(c, err)
		return
	}
	fingerprint, err := s.discord.SaveDevelopmentEnvironmentSSH(c, id, input)
	if err != nil {
		problem(c, http.StatusConflict, "保存开发环境 SSH 配置失败", err)
		return
	}
	s.audit(c, "development_environment.ssh.update", "development_environment",
		id.String(), map[string]any{"port": input.Port, "fingerprint": fingerprint,
			"discordUserId": input.DiscordUserID})
	c.Status(http.StatusAccepted)
}

func (s *Server) deleteDevelopmentEnvironmentSSH(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	if err := s.discord.ClearDevelopmentEnvironmentSSH(c, id); err != nil {
		problem(c, http.StatusConflict, "停用开发环境 SSH 失败", err)
		return
	}
	s.audit(c, "development_environment.ssh.delete", "development_environment",
		id.String(), nil)
	c.Status(http.StatusAccepted)
}

func (s *Server) createDevelopmentProjectForum(c *gin.Context) {
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
		if err := s.discord.RestoreDevelopmentForum(c, projectID, *input.ForumID); err != nil {
			problem(c, http.StatusConflict, "恢复开发 Forum 失败", err)
			return
		}
		s.audit(c, "development_forum.restore", "discord_forum",
			input.ForumID.String(), map[string]any{"developmentProjectId": projectID})
		c.Status(http.StatusAccepted)
	case "new":
		guild, err := s.discordGuild(c)
		if err != nil {
			problem(c, http.StatusBadGateway, "读取 Discord Guild 失败", err)
			return
		}
		plan, err := s.discord.DevelopmentProjectForumPlan(c, guild, projectID, input.Name)
		if err != nil {
			problem(c, http.StatusConflict, "创建开发 Forum 预检失败", err)
			return
		}
		administratorID := c.MustGet("session").(auth.Session).AdministratorID
		operationID, err := s.discord.CreateInitialization(c, administratorID, plan, "")
		if err != nil {
			problem(c, http.StatusConflict, "创建开发 Forum 失败", err)
			return
		}
		s.audit(c, "development_forum.create", "development_project", projectID.String(),
			map[string]any{"operationId": operationID})
		c.JSON(http.StatusAccepted, gin.H{"id": operationID})
	default:
		badRequest(c, errors.New("mode 必须是 new 或 restore"))
	}
}

func (s *Server) disableDevelopmentForum(c *gin.Context) {
	s.setDevelopmentForumEnabled(c, false)
}

func (s *Server) enableDevelopmentForum(c *gin.Context) {
	s.setDevelopmentForumEnabled(c, true)
}

func (s *Server) setDevelopmentForumEnabled(c *gin.Context, enabled bool) {
	forumID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var err error
	if enabled {
		err = s.discord.EnableDevelopmentForum(c, forumID)
	} else {
		err = s.discord.DisableDevelopmentForum(c, forumID)
	}
	if err != nil {
		problem(c, http.StatusConflict, "更新开发 Forum 配对状态失败", err)
		return
	}
	action := "disable"
	if enabled {
		action = "enable"
	}
	s.audit(c, "development_forum."+action, "discord_forum", forumID.String(), nil)
	c.Status(http.StatusAccepted)
}

func (s *Server) putDevelopmentProjectForumCollaborator(c *gin.Context) {
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
	if err := s.discord.SetDevelopmentProjectForumAccess(c, projectID, forumID,
		c.Param("memberId"), input.AccessLevel, administratorID); err != nil {
		badRequest(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) deleteDevelopmentProjectForumCollaborator(c *gin.Context) {
	projectID, forumID, ok := parseProjectForumParams(c)
	if !ok {
		return
	}
	if err := s.discord.DeleteDevelopmentProjectForumAccess(
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
