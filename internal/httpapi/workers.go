package httpapi

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/workerregistry"
)

type createWorkerRequest struct {
	Name              string   `json:"name" binding:"required"`
	Roles             []string `json:"roles" binding:"required"`
	MaxConcurrentJobs int      `json:"maxConcurrentJobs"`
}

func (s *Server) listWorkers(c *gin.Context) {
	workers, err := s.workers.List(c.Request.Context())
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取Worker失败", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": workers})
}

func (s *Server) createWorker(c *gin.Context) {
	var request createWorkerRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	for _, role := range request.Roles {
		if role == "github" {
			problem(c, http.StatusGone, "GitHub 功能已停用", nil)
			return
		}
	}
	worker, token, err := s.workers.Create(c.Request.Context(), request.Name, request.Roles,
		request.MaxConcurrentJobs)
	if err != nil {
		problem(c, http.StatusConflict, "创建Worker失败", err)
		return
	}
	s.audit(c, "worker.create", "worker", worker.ID.String(), map[string]any{
		"name": worker.Name, "roles": worker.Roles,
	})
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusCreated, gin.H{"worker": worker, "enrollmentToken": token, "expiresIn": 900})
}

func (s *Server) createWorkerEnrollment(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	token, err := s.workers.NewEnrollment(c.Request.Context(), id)
	if err != nil {
		problem(c, http.StatusConflict, "创建节点注册凭据失败", err)
		return
	}
	s.audit(c, "worker.enrollment.create", "worker", id.String(), nil)
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusCreated, gin.H{"enrollmentToken": token, "expiresIn": 900})
}

func (s *Server) setWorkerEnabled(c *gin.Context) {
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
	if err := s.workers.SetEnabled(c.Request.Context(), id, *request.Enabled); err != nil {
		status := http.StatusConflict
		if errors.Is(err, sql.ErrNoRows) {
			status = http.StatusNotFound
		}
		problem(c, status, "更新Worker失败", err)
		return
	}
	s.audit(c, "worker.enabled.update", "worker", id.String(), map[string]any{
		"enabled": *request.Enabled,
	})
	c.Status(http.StatusNoContent)
}

func (s *Server) deleteWorker(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	if err := s.workers.Delete(c.Request.Context(), id); err != nil {
		problem(c, http.StatusConflict, "删除Worker失败", err)
		return
	}
	s.audit(c, "worker.delete", "worker", id.String(), nil)
	c.Status(http.StatusNoContent)
}

func (s *Server) getWorkerSettings(c *gin.Context) {
	settings, err := s.workers.Defaults(c.Request.Context())
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取Worker设置失败", err)
		return
	}
	c.JSON(http.StatusOK, settings)
}

func (s *Server) putWorkerSettings(c *gin.Context) {
	var request workerregistry.Defaults
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	if err := s.workers.SetDefaults(c.Request.Context(), request); err != nil {
		problem(c, http.StatusConflict, "保存Worker设置失败", err)
		return
	}
	s.audit(c, "worker_defaults.update", "worker_settings", "", map[string]any{
		"githubWorkerId": request.GitHubWorkerID, "discordWorkerId": request.DiscordWorkerID,
	})
	c.Status(http.StatusNoContent)
}
