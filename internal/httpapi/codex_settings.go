package httpapi

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexsettings"
)

func (s *Server) listCodexSettings(c *gin.Context) {
	items, err := codexsettings.NewService(s.db).List(c)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Codex 设置失败", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "modelOptions": codexsettings.PresetModels})
}

func (s *Server) putRepositoryCodexSettings(c *gin.Context) {
	s.putCodexSettings(c, codexsettings.ScopeRepository, "repository")
}

func (s *Server) putForumCodexSettings(c *gin.Context) {
	s.putCodexSettings(c, codexsettings.ScopeDiscordForum, "discord_forum")
}

func (s *Server) putCodexSettings(c *gin.Context, scope, resourceType string) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	var input codexsettings.Preferences
	if err := c.ShouldBindJSON(&input); err != nil {
		badRequest(c, err)
		return
	}
	if err := codexsettings.NewService(s.db).Save(c, scope, id, input); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			problem(c, http.StatusNotFound, "Codex 设置范围不存在", err)
			return
		}
		badRequest(c, err)
		return
	}
	s.audit(c, "codex.settings.update", resourceType, id.String(), map[string]any{"scope": scope})
	c.Status(http.StatusNoContent)
}
