package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/slovx2/tyrs-hand/internal/codexauth"
	platformsettings "github.com/slovx2/tyrs-hand/internal/settings"
)

func (s *Server) getAgentProviderSettings(c *gin.Context) {
	settings, err := s.settings.AgentProvider(c.Request.Context())
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Agent Provider 设置失败", err)
		return
	}
	account, err := s.codexAuth.Account(c.Request.Context())
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取全局 ChatGPT 账号失败", err)
		return
	}
	c.JSON(http.StatusOK, struct {
		platformsettings.AgentProvider
		ChatGPTAccount codexauth.Account `json:"chatgptAccount"`
	}{AgentProvider: settings, ChatGPTAccount: account})
}

func (s *Server) putAgentProviderSettings(c *gin.Context) {
	var input platformsettings.AgentProviderInput
	if err := c.ShouldBindJSON(&input); err != nil {
		badRequest(c, err)
		return
	}
	if err := s.settings.SaveAgentProvider(c.Request.Context(), input); err != nil {
		badRequest(c, err)
		return
	}
	s.audit(c, "settings.agent_provider.update", "platform_setting", "agent.provider", nil)
	c.Status(http.StatusNoContent)
}

func (s *Server) getGlobalAgents(c *gin.Context) {
	value, err := s.settings.GlobalAgents(c.Request.Context())
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取全局 AGENTS.md 失败", err)
		return
	}
	c.JSON(http.StatusOK, value)
}

func (s *Server) putGlobalAgents(c *gin.Context) {
	var input platformsettings.GlobalAgents
	if err := c.ShouldBindJSON(&input); err != nil {
		badRequest(c, err)
		return
	}
	if err := s.settings.SaveGlobalAgents(c.Request.Context(), input); err != nil {
		badRequest(c, err)
		return
	}
	s.audit(c, "settings.global_agents.update", "platform_setting", "codex.global_agents",
		map[string]any{"size": len(input.Content)})
	c.Status(http.StatusNoContent)
}
