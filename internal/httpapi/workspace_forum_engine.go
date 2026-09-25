package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
)

func (s *Server) putWorkspaceForumEngine(c *gin.Context) {
	forumID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	var input struct {
		Engine runtimeidentity.Engine `json:"engine" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		badRequest(c, err)
		return
	}
	if err := input.Engine.Validate(); err != nil {
		badRequest(c, err)
		return
	}
	if !s.requireForumWorkerAccess(c, forumID) {
		return
	}
	if err := s.discord.SetWorkspaceForumEngine(c.Request.Context(), forumID, input.Engine); err != nil {
		problem(c, http.StatusConflict, "更新论坛默认引擎失败", err)
		return
	}
	s.audit(c, "workspace_forum.engine", "discord_forum", forumID.String(), map[string]any{"engine": input.Engine})
	c.Status(http.StatusNoContent)
}
