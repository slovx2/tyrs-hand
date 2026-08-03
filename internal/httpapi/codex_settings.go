package httpapi

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcatalog"
	"github.com/slovx2/tyrs-hand/internal/codexsettings"
)

func (s *Server) listGitHubAgentSettings(c *gin.Context) {
	items, err := codexsettings.NewService(s.db).List(c)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 GitHub Agent 设置失败", err)
		return
	}
	catalogs, err := codexcatalog.OnlineCatalogs(c, s.db)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Codex 模型目录失败", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items,
		"models": codexcatalog.Models(catalogs)})
}

func (s *Server) putRepositoryGitHubAgentSettings(c *gin.Context) {
	s.putGitHubAgentSettings(c, "repository")
}

func (s *Server) putGitHubAgentSettings(c *gin.Context, resourceType string) {
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
	if err := codexsettings.NewService(s.db).Save(c, id, input); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			problem(c, http.StatusNotFound, "GitHub Agent 设置范围不存在", err)
			return
		}
		badRequest(c, err)
		return
	}
	s.audit(c, "github_agent.settings.update", resourceType, id.String(), nil)
	c.Status(http.StatusNoContent)
}
