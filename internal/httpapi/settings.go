package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
	platformsettings "github.com/slovx2/tyrs-hand/internal/settings"
)

func (s *Server) getGitHubAgentInstructions(c *gin.Context) {
	value, err := s.settings.GitHubAgentInstructions(c.Request.Context())
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取全局 AGENTS.md 失败", err)
		return
	}
	c.JSON(http.StatusOK, value)
}

func (s *Server) putGitHubAgentInstructions(c *gin.Context) {
	var input platformsettings.GitHubAgentInstructions
	if err := c.ShouldBindJSON(&input); err != nil {
		badRequest(c, err)
		return
	}
	if err := s.settings.SaveGitHubAgentInstructions(c.Request.Context(), input); err != nil {
		badRequest(c, err)
		return
	}
	s.audit(c, "settings.github_agent_instructions.update", "platform_setting",
		"github.agent.instructions",
		map[string]any{"size": len(input.Content)})
	c.Status(http.StatusNoContent)
}
