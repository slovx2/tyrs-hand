package httpapi

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/executionnode"
)

type createExecutionNodeRequest struct {
	Name              string   `json:"name" binding:"required"`
	Roles             []string `json:"roles" binding:"required"`
	MaxConcurrentJobs int      `json:"maxConcurrentJobs"`
}

func (s *Server) listExecutionNodes(c *gin.Context) {
	nodes, err := s.nodes.List(c.Request.Context())
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取执行节点失败", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": nodes})
}

func (s *Server) createExecutionNode(c *gin.Context) {
	var request createExecutionNodeRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	node, token, err := s.nodes.Create(c.Request.Context(), request.Name, request.Roles,
		request.MaxConcurrentJobs)
	if err != nil {
		problem(c, http.StatusConflict, "创建执行节点失败", err)
		return
	}
	s.audit(c, "execution_node.create", "execution_node", node.ID.String(), map[string]any{
		"name": node.Name, "roles": node.Roles,
	})
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusCreated, gin.H{"node": node, "enrollmentToken": token, "expiresIn": 900})
}

func (s *Server) createExecutionNodeEnrollment(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	token, err := s.nodes.NewEnrollment(c.Request.Context(), id)
	if err != nil {
		problem(c, http.StatusConflict, "创建节点注册凭据失败", err)
		return
	}
	s.audit(c, "execution_node.enrollment.create", "execution_node", id.String(), nil)
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusCreated, gin.H{"enrollmentToken": token, "expiresIn": 900})
}

func (s *Server) setExecutionNodeEnabled(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	var request struct {
		Enabled *bool `json:"enabled" binding:"required"`
	}
	if err := c.ShouldBindJSON(&request); err != nil || request.Enabled == nil {
		badRequest(c, errors.New("必须提供 enabled"))
		return
	}
	if err := s.nodes.SetEnabled(c.Request.Context(), id, *request.Enabled); err != nil {
		status := http.StatusConflict
		if errors.Is(err, sql.ErrNoRows) {
			status = http.StatusNotFound
		}
		problem(c, status, "更新执行节点失败", err)
		return
	}
	s.audit(c, "execution_node.enabled.update", "execution_node", id.String(), map[string]any{
		"enabled": *request.Enabled,
	})
	c.Status(http.StatusNoContent)
}

func (s *Server) deleteExecutionNode(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	if err := s.nodes.Delete(c.Request.Context(), id); err != nil {
		problem(c, http.StatusConflict, "删除执行节点失败", err)
		return
	}
	s.audit(c, "execution_node.delete", "execution_node", id.String(), nil)
	c.Status(http.StatusNoContent)
}

func (s *Server) getExecutionSettings(c *gin.Context) {
	settings, err := s.nodes.Defaults(c.Request.Context())
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取执行节点设置失败", err)
		return
	}
	c.JSON(http.StatusOK, settings)
}

func (s *Server) putExecutionSettings(c *gin.Context) {
	var request executionnode.Defaults
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	if err := s.nodes.SetDefaults(c.Request.Context(), request); err != nil {
		problem(c, http.StatusConflict, "保存执行节点设置失败", err)
		return
	}
	s.audit(c, "execution_defaults.update", "execution_settings", "", map[string]any{
		"githubNodeId": request.GitHubNodeID, "discordNodeId": request.DiscordNodeID,
	})
	c.Status(http.StatusNoContent)
}
