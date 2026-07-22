package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/sshconfig"
)

func parseResourceID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return uuid.Nil, false
	}
	return id, true
}

func (s *Server) listSSHCredentials(c *gin.Context) {
	items, err := s.ssh.ListCredentials(c.Request.Context())
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 SSH 凭证失败", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (s *Server) createSSHCredential(c *gin.Context) {
	var input sshconfig.CredentialInput
	if err := c.ShouldBindJSON(&input); err != nil {
		badRequest(c, err)
		return
	}
	item, err := s.ssh.CreateCredential(c.Request.Context(), input)
	if err != nil {
		problem(c, http.StatusConflict, "创建 SSH 凭证失败", err)
		return
	}
	s.audit(c, "ssh_credential.create", "ssh_credential", item.ID.String(),
		map[string]any{"name": item.Name, "fingerprint": item.Fingerprint})
	c.JSON(http.StatusCreated, item)
}

func (s *Server) updateSSHCredential(c *gin.Context) {
	id, ok := parseResourceID(c)
	if !ok {
		return
	}
	var input sshconfig.CredentialInput
	if err := c.ShouldBindJSON(&input); err != nil {
		badRequest(c, err)
		return
	}
	rotated := strings.TrimSpace(input.PrivateKey) != ""
	item, err := s.ssh.UpdateCredential(c.Request.Context(), id, input)
	if err != nil {
		status := http.StatusConflict
		if errors.Is(err, sql.ErrNoRows) {
			status = http.StatusNotFound
		}
		problem(c, status, "更新 SSH 凭证失败", err)
		return
	}
	s.audit(c, "ssh_credential.update", "ssh_credential", id.String(),
		map[string]any{"name": item.Name, "rotated": rotated, "fingerprint": item.Fingerprint})
	c.JSON(http.StatusOK, item)
}

func (s *Server) deleteSSHCredential(c *gin.Context) {
	id, ok := parseResourceID(c)
	if !ok {
		return
	}
	if err := s.ssh.DeleteCredential(c.Request.Context(), id); err != nil {
		problem(c, http.StatusConflict, "删除 SSH 凭证失败", err)
		return
	}
	s.audit(c, "ssh_credential.delete", "ssh_credential", id.String(), nil)
	c.Status(http.StatusNoContent)
}

func (s *Server) listSSHHosts(c *gin.Context) {
	items, err := s.ssh.ListHosts(c.Request.Context())
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 SSH 主机失败", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (s *Server) createSSHHost(c *gin.Context) {
	var input sshconfig.HostInput
	if err := c.ShouldBindJSON(&input); err != nil {
		badRequest(c, err)
		return
	}
	item, err := s.ssh.CreateHost(c.Request.Context(), input)
	if err != nil {
		problem(c, http.StatusConflict, "创建 SSH 主机失败", err)
		return
	}
	s.audit(c, "ssh_host.create", "ssh_host", item.ID.String(),
		map[string]any{"alias": item.Alias, "executionNodeIds": item.ExecutionNodeIDs})
	c.JSON(http.StatusCreated, item)
}

func (s *Server) updateSSHHost(c *gin.Context) {
	id, ok := parseResourceID(c)
	if !ok {
		return
	}
	var input sshconfig.HostInput
	if err := c.ShouldBindJSON(&input); err != nil {
		badRequest(c, err)
		return
	}
	item, err := s.ssh.UpdateHost(c.Request.Context(), id, input)
	if err != nil {
		status := http.StatusConflict
		if errors.Is(err, sql.ErrNoRows) {
			status = http.StatusNotFound
		}
		problem(c, status, "更新 SSH 主机失败", err)
		return
	}
	s.audit(c, "ssh_host.update", "ssh_host", id.String(),
		map[string]any{"alias": item.Alias, "executionNodeIds": item.ExecutionNodeIDs})
	c.JSON(http.StatusOK, item)
}

func (s *Server) deleteSSHHost(c *gin.Context) {
	id, ok := parseResourceID(c)
	if !ok {
		return
	}
	if err := s.ssh.DeleteHost(c.Request.Context(), id); err != nil {
		problem(c, http.StatusConflict, "删除 SSH 主机失败", err)
		return
	}
	s.audit(c, "ssh_host.delete", "ssh_host", id.String(), nil)
	c.Status(http.StatusNoContent)
}
