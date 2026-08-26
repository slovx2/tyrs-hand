package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/auth"
)

type setupRequest struct {
	SetupToken string `json:"setupToken" binding:"required"`
	Username   string `json:"username" binding:"required"`
	Password   string `json:"password" binding:"required"`
}

type loginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
	TOTP     string `json:"totp" binding:"required"`
}

type inviteRequest struct {
	Username string `json:"username" binding:"required"`
}

type acceptInviteRequest struct {
	Token    string `json:"token" binding:"required"`
	Password string `json:"password" binding:"required"`
}

func (s *Server) setupStatus(c *gin.Context) {
	required, err := s.auth.SetupRequired(c.Request.Context())
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Setup 状态失败", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"setupRequired": required})
}

func (s *Server) setupAdmin(c *gin.Context) {
	var request setupRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	result, err := s.auth.Setup(c.Request.Context(), request.SetupToken, request.Username, request.Password)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, auth.ErrInvalidSetupToken) {
			status = http.StatusUnauthorized
		} else if errors.Is(err, auth.ErrSetupComplete) {
			status = http.StatusConflict
		}
		problem(c, status, "创建管理员失败", err)
		return
	}
	c.JSON(http.StatusCreated, result)
}

func (s *Server) login(c *gin.Context) {
	var request loginRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	session, err := s.auth.Login(c.Request.Context(), request.Username, request.Password, request.TOTP)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, auth.ErrInvalidCredentials) {
			status = http.StatusUnauthorized
		}
		problem(c, status, "登录失败", err)
		return
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name: sessionCookie, Value: session.Token, Path: "/", Expires: session.ExpiresAt,
		HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteStrictMode,
	})
	c.JSON(http.StatusOK, gin.H{"id": session.AdministratorID, "username": session.Username, "csrfToken": session.CSRFToken, "expiresAt": session.ExpiresAt, "role": session.Role})
}

func (s *Server) createInvitation(c *gin.Context) {
	var request inviteRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	session := c.MustGet("session").(auth.Session)
	invitation, err := s.auth.CreateInvitation(c.Request.Context(), session.AdministratorID, request.Username, 72*time.Hour)
	if err != nil {
		problem(c, http.StatusInternalServerError, "创建邀请失败", err)
		return
	}
	c.JSON(http.StatusCreated, invitation)
}

func (s *Server) listInvitations(c *gin.Context) {
	items, err := s.auth.ListInvitations(c.Request.Context())
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取邀请失败", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (s *Server) revokeInvitation(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	if err := s.auth.RevokeInvitation(c.Request.Context(), id); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, auth.ErrInvitationNotFound) {
			status = http.StatusNotFound
		}
		problem(c, status, "撤销邀请失败", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) acceptInvitation(c *gin.Context) {
	var request acceptInviteRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	result, err := s.auth.AcceptInvitation(c.Request.Context(), request.Token, request.Password, "")
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, auth.ErrInvitationInvalid) {
			status = http.StatusUnauthorized
		}
		problem(c, status, "接受邀请失败", err)
		return
	}
	c.JSON(http.StatusCreated, result)
}

func (s *Server) me(c *gin.Context) {
	session := c.MustGet("session").(auth.Session)
	role, err := s.auth.Role(c.Request.Context(), session.AdministratorID)
	if err != nil {
		problem(c, http.StatusUnauthorized, "登录会话无效", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"id": session.AdministratorID, "username": session.Username,
		"csrfToken": session.CSRFToken, "expiresAt": session.ExpiresAt, "role": role,
	})
}

func (s *Server) logout(c *gin.Context) {
	token, _ := c.Cookie(sessionCookie)
	if err := s.auth.Logout(c.Request.Context(), token); err != nil {
		problem(c, http.StatusInternalServerError, "退出失败", err)
		return
	}
	http.SetCookie(c.Writer, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, Expires: time.Unix(1, 0), HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteStrictMode})
	c.Status(http.StatusNoContent)
}

func (s *Server) requireSession() gin.HandlerFunc {
	return func(c *gin.Context) {
		token, err := c.Cookie(sessionCookie)
		if err != nil {
			problem(c, http.StatusUnauthorized, "需要登录", auth.ErrSessionInvalid)
			return
		}
		session, err := s.auth.Authenticate(c.Request.Context(), token)
		if err != nil {
			problem(c, http.StatusUnauthorized, "登录会话无效", err)
			return
		}
		session.Role, err = s.auth.Role(c.Request.Context(), session.AdministratorID)
		if err != nil {
			problem(c, http.StatusUnauthorized, "登录会话无效", err)
			return
		}
		c.Set("session", session)
		c.Next()
	}
}

func (s *Server) requireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		session := c.MustGet("session").(auth.Session)
		if session.Role != "admin" {
			problem(c, http.StatusForbidden, "需要管理员权限", nil)
			return
		}
		c.Next()
	}
}

func (s *Server) requireCSRF() gin.HandlerFunc {
	return func(c *gin.Context) {
		token, _ := c.Cookie(sessionCookie)
		csrf := c.GetHeader("X-CSRF-Token")
		if !s.auth.ValidateCSRF(c.Request.Context(), token, csrf) {
			problem(c, http.StatusForbidden, "CSRF 校验失败", nil)
			return
		}
		c.Next()
	}
}
