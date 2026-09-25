package httpapi

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/auth"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
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

func (s *Server) discordGuild(c *gin.Context) (discordintegration.RemoteGuild, error) {
	settings, err := s.discord.Settings(c)
	if err != nil || settings.GuildID == "" || !settings.TokenConfigured {
		return discordintegration.RemoteGuild{}, errors.New("discord Guild 或 Bot Token 尚未配置")
	}
	token, err := s.discord.BotToken(c)
	if err != nil {
		return discordintegration.RemoteGuild{}, err
	}
	remote := discordintegration.NewDisgoRemote(token, "", nil)
	defer remote.Close(c)
	return remote.Guild(c, settings.GuildID)
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
	settings, err := s.discord.Settings(c)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Discord 设置失败", err)
		return
	}
	if settings.GuildID != "" && settings.TokenConfigured {
		token, tokenErr := s.discord.BotToken(c)
		if tokenErr != nil {
			problem(c, http.StatusInternalServerError, "读取 Discord Bot 配置失败", tokenErr)
			return
		}
		remote := discordintegration.NewDisgoRemote(token, "", nil)
		defer remote.Close(c)
		remoteMembers, syncErr := remote.Members(c, settings.GuildID)
		if syncErr != nil {
			problem(c, http.StatusBadGateway, "同步 Discord 成员失败", syncErr)
			return
		}
		if syncErr := s.discord.ReplaceMembers(c, settings.GuildID, remoteMembers); syncErr != nil {
			problem(c, http.StatusInternalServerError, "保存 Discord 成员失败", syncErr)
			return
		}
	}
	members, err := s.discord.Members(c)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Discord 成员失败", err)
		return
	}
	c.JSON(http.StatusOK, members)
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
