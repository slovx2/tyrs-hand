package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
	platformsettings "github.com/slovx2/tyrs-hand/internal/settings"
)

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
