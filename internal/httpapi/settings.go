package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
	platformsettings "github.com/slovx2/tyrs-hand/internal/settings"
)

func (s *Server) getAgentProviderSettings(c *gin.Context) {
	settings, err := s.settings.AgentProvider(c.Request.Context())
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Agent Provider 设置失败", err)
		return
	}
	c.JSON(http.StatusOK, settings)
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
