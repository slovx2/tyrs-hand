package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/auth"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"go.uber.org/zap"
)

func (s *Server) getDiscordSettings(c *gin.Context) {
	settings, err := s.discord.Settings(c)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Discord 设置失败", err)
		return
	}
	c.JSON(http.StatusOK, settings)
}

func (s *Server) putDiscordSettings(c *gin.Context) {
	var input discordintegration.SettingsInput
	if err := c.ShouldBindJSON(&input); err != nil {
		badRequest(c, err)
		return
	}
	if err := s.discord.SaveSettings(c, input); err != nil {
		badRequest(c, err)
		return
	}
	s.audit(c, "discord.settings.update", "discord_guild", input.GuildID, map[string]any{"enabled": input.Enabled})
	c.Status(http.StatusNoContent)
}

func (s *Server) discordStatus(c *gin.Context) {
	status, err := s.discord.Status(c)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Discord 状态失败", err)
		return
	}
	c.JSON(http.StatusOK, status)
}

type initializationRequest struct {
	Mode         string `json:"mode"`
	Confirmation string `json:"confirmation"`
}

func (s *Server) discordInitializationPreflight(c *gin.Context) {
	var input initializationRequest
	if err := c.ShouldBindJSON(&input); err != nil {
		badRequest(c, err)
		return
	}
	plan, err := s.discordPreflight(c, input.Mode)
	if err != nil {
		badRequest(c, err)
		return
	}
	c.JSON(http.StatusOK, plan.Preflight)
}

func (s *Server) createDiscordInitialization(c *gin.Context) {
	var input initializationRequest
	if err := c.ShouldBindJSON(&input); err != nil {
		badRequest(c, err)
		return
	}
	plan, err := s.discordPreflight(c, input.Mode)
	if err != nil {
		badRequest(c, err)
		return
	}
	administratorID := c.MustGet("session").(auth.Session).AdministratorID
	operationID, err := s.discord.CreateInitialization(c, administratorID, plan, input.Confirmation)
	if err != nil {
		problem(c, http.StatusConflict, "创建 Discord 初始化失败", err)
		return
	}
	s.audit(c, "discord.initialization.create", "discord_initialization", operationID.String(),
		map[string]any{"mode": input.Mode, "guildId": plan.Preflight.GuildID})
	c.JSON(http.StatusAccepted, gin.H{"id": operationID})
}

func (s *Server) discordPreflight(c *gin.Context, mode string) (discordintegration.InitializationPlan, error) {
	if mode == "" {
		mode = discordintegration.InitializationIncremental
	}
	settings, err := s.discord.Settings(c)
	if err != nil || settings.GuildID == "" || !settings.TokenConfigured {
		return discordintegration.InitializationPlan{}, errors.New("discord Guild 或 Bot Token 尚未配置")
	}
	token, err := s.discord.BotToken(c)
	if err != nil {
		return discordintegration.InitializationPlan{}, err
	}
	remote := discordintegration.NewDisgoRemote(token, "", nil)
	defer remote.Close(c)
	guild, err := remote.Guild(c, settings.GuildID)
	if err != nil {
		return discordintegration.InitializationPlan{}, err
	}
	return s.discord.ServerInitializationPlan(c, guild, mode)
}

func (s *Server) getDiscordInitialization(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	operation, err := s.discord.Operation(c, id)
	if err != nil {
		problem(c, http.StatusNotFound, "Discord 初始化不存在", err)
		return
	}
	c.JSON(http.StatusOK, operation)
}

func (s *Server) listDiscordMembers(c *gin.Context) {
	members, err := s.discord.Members(c)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Discord 成员失败", err)
		return
	}
	c.JSON(http.StatusOK, members)
}

func (s *Server) listDiscordDevelopmentEnvironments(c *gin.Context) {
	environments, err := s.discord.DevelopmentEnvironments(c)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Discord 开发环境失败", err)
		return
	}
	c.JSON(http.StatusOK, environments)
}

func (s *Server) rebuildDiscordDevelopmentEnvironment(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	if err := s.discord.RebuildDevelopmentEnvironment(c, id); err != nil {
		problem(c, http.StatusConflict, "重建 Discord 开发环境失败", err)
		return
	}
	s.audit(c, "discord.development_environment.rebuild", "discord_development_environment", id.String(), nil)
	c.Status(http.StatusAccepted)
}

func (s *Server) putDiscordDevelopmentEnvironmentSSH(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
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
	s.audit(c, "discord.development_environment.ssh.update", "discord_development_environment",
		id.String(), map[string]any{"port": input.Port, "fingerprint": fingerprint,
			"discordUserId": input.DiscordUserID})
	c.Status(http.StatusAccepted)
}

func (s *Server) deleteDiscordDevelopmentEnvironmentSSH(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	if err := s.discord.ClearDevelopmentEnvironmentSSH(c, id); err != nil {
		problem(c, http.StatusConflict, "停用开发环境 SSH 失败", err)
		return
	}
	s.audit(c, "discord.development_environment.ssh.delete", "discord_development_environment",
		id.String(), nil)
	c.Status(http.StatusAccepted)
}

func (s *Server) discordDevelopmentForumDeletePreflight(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	preflight, err := s.discord.DevelopmentForumDeletePreflight(c, id)
	if err != nil {
		problem(c, http.StatusNotFound, "Discord 开发 Forum 不存在", err)
		return
	}
	c.JSON(http.StatusOK, preflight)
}

func (s *Server) deleteDiscordDevelopmentForum(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	var input struct {
		Confirmation string `json:"confirmation" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		badRequest(c, err)
		return
	}
	administratorID := c.MustGet("session").(auth.Session).AdministratorID
	operationID, err := s.discord.DeleteDevelopmentForum(c, id, input.Confirmation, administratorID)
	if err != nil {
		problem(c, http.StatusConflict, "删除 Discord 开发 Forum 失败", err)
		return
	}
	s.audit(c, "discord.development_forum.delete", "discord_forum", id.String(),
		map[string]any{"operationId": operationID})
	c.JSON(http.StatusAccepted, gin.H{"id": operationID})
}

func (s *Server) createDiscordMemberForum(c *gin.Context) {
	var input struct {
		RepositoryID uuid.UUID `json:"repositoryId" binding:"required"`
		Name         string    `json:"name"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		badRequest(c, err)
		return
	}
	settings, err := s.discord.Settings(c)
	if err != nil || settings.GuildID == "" {
		badRequest(c, errors.New("discord 尚未配置"))
		return
	}
	token, err := s.discord.BotToken(c)
	if err != nil {
		badRequest(c, err)
		return
	}
	remote := discordintegration.NewDisgoRemote(token, "", nil)
	defer remote.Close(c)
	guild, err := remote.Guild(c, settings.GuildID)
	if err != nil {
		problem(c, http.StatusBadGateway, "读取 Discord Guild 失败", err)
		return
	}
	plan, err := s.discord.DevelopmentForumPlan(c, guild, c.Param("id"), input.RepositoryID, input.Name)
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
	s.audit(c, "discord.development_forum.create", "discord_member", c.Param("id"),
		map[string]any{"operationId": operationID, "repositoryId": input.RepositoryID})
	c.JSON(http.StatusAccepted, gin.H{"id": operationID})
}

func (s *Server) putDiscordForumAccess(c *gin.Context) {
	forumID, err := uuid.Parse(c.Param("forumId"))
	if err != nil {
		badRequest(c, err)
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
	if err := s.discord.SetForumAccess(c, forumID, c.Param("memberId"), input.AccessLevel, administratorID); err != nil {
		badRequest(c, err)
		return
	}
	s.audit(c, "discord.forum_access.update", "discord_forum", forumID.String(),
		map[string]any{"memberId": c.Param("memberId"), "accessLevel": input.AccessLevel})
	c.Status(http.StatusNoContent)
}

func (s *Server) deleteDiscordForumAccess(c *gin.Context) {
	forumID, err := uuid.Parse(c.Param("forumId"))
	if err != nil {
		badRequest(c, err)
		return
	}
	if err := s.discord.DeleteForumAccess(c, forumID, c.Param("memberId")); err != nil {
		badRequest(c, err)
		return
	}
	s.audit(c, "discord.forum_access.delete", "discord_forum", forumID.String(), map[string]any{"memberId": c.Param("memberId")})
	c.Status(http.StatusNoContent)
}

func (s *Server) startDiscordGitHubBind(c *gin.Context) {
	var input struct {
		GuildID string `json:"guildId" binding:"required"`
		UserID  string `json:"discordUserId" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		badRequest(c, err)
		return
	}
	link, err := s.bindings.Start(c, input.GuildID, input.UserID)
	if err != nil {
		badRequest(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"url": link})
}

func (s *Server) discordGitHubBindCallback(c *gin.Context) {
	binding, err := s.bindings.Callback(c, c.Query("state"), c.Query("code"))
	if err != nil {
		problem(c, http.StatusForbidden, "GitHub 身份绑定失败", err)
		return
	}
	if s.redis != nil {
		message, marshalErr := json.Marshal(map[string]string{"discordUserId": binding.DiscordUserID})
		if marshalErr != nil {
			s.logger.Warn("编码 Discord 用户仓库权限同步事件失败", zap.Error(marshalErr))
		} else if publishErr := s.redis.Publish(c.Request.Context(), discordintegration.RepositoryPermissionSyncChannel, message).Err(); publishErr != nil {
			// 定时全量同步会在 Redis 暂时不可用时兜底。
			s.logger.Warn("发布 Discord 用户仓库权限同步事件失败", zap.Error(publishErr))
		}
	}
	c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte("GitHub 身份绑定成功："+binding.GitHubLogin+"。可以关闭此页面。"))
}

func (s *Server) unbindDiscordGitHub(c *gin.Context) {
	var input struct {
		GuildID   string `json:"guildId" binding:"required"`
		UserID    string `json:"discordUserId" binding:"required"`
		Confirmed bool   `json:"confirmed"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		badRequest(c, err)
		return
	}
	if err := s.bindings.Unbind(c, input.GuildID, input.UserID, input.Confirmed); err != nil {
		badRequest(c, err)
		return
	}
	s.audit(c, "discord.github.unbind", "discord_member", input.UserID, nil)
	c.Status(http.StatusNoContent)
}
