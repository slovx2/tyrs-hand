package httpapi

import (
	"database/sql"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/auth"
)

func (s *Server) startChatGPTLogin(c *gin.Context) {
	session := c.MustGet("session").(auth.Session)
	operation, err := s.codexAuth.Start(c.Request.Context(), session.AdministratorID)
	if err != nil {
		problem(c, http.StatusConflict, "发起 ChatGPT Device Code 登录失败", err)
		return
	}
	s.audit(c, "settings.chatgpt.login.start", "codex_auth_operation",
		operation.ID.String(), nil)
	c.JSON(http.StatusAccepted, operation)
}

func (s *Server) getChatGPTLogin(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	operation, err := s.codexAuth.Get(c.Request.Context(), id)
	if err != nil {
		status := http.StatusInternalServerError
		if err == sql.ErrNoRows {
			status = http.StatusNotFound
		}
		problem(c, status, "读取 ChatGPT 登录状态失败", err)
		return
	}
	c.JSON(http.StatusOK, operation)
}

func (s *Server) cancelChatGPTLogin(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	if err := s.codexAuth.Cancel(c.Request.Context(), id); err != nil {
		problem(c, http.StatusConflict, "取消 ChatGPT 登录失败", err)
		return
	}
	s.audit(c, "settings.chatgpt.login.cancel", "codex_auth_operation", id.String(), nil)
	c.Status(http.StatusNoContent)
}

func (s *Server) logoutChatGPT(c *gin.Context) {
	if err := s.codexAuth.Logout(c.Request.Context()); err != nil {
		problem(c, http.StatusConflict, "退出全局 ChatGPT 账号失败", err)
		return
	}
	s.audit(c, "settings.chatgpt.logout", "platform_setting", "agent.provider", nil)
	c.Status(http.StatusNoContent)
}
