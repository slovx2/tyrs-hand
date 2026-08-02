package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/slovx2/tyrs-hand/internal/auth"
)

const clientBearerWebSocketPrefix = "tyrs-hand.bearer."
const clientWebSocketProtocolContext = "clientWebSocketProtocol"

func (s *Server) clientLogin(c *gin.Context) {
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
		problem(c, status, "客户端登录失败", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"accessToken": session.Token,
		"tokenType":   "Bearer",
		"expiresAt":   session.ExpiresAt,
		"user": gin.H{
			"id": session.AdministratorID, "username": session.Username,
		},
	})
}

func (s *Server) requireClientBearer() gin.HandlerFunc {
	return func(c *gin.Context) {
		token, protocol := clientBearerCredentials(c.Request)
		if token == "" {
			problem(c, http.StatusUnauthorized, "需要 Bearer Token", auth.ErrSessionInvalid)
			return
		}
		session, err := s.auth.Authenticate(c.Request.Context(), token)
		if err != nil {
			problem(c, http.StatusUnauthorized, "客户端会话无效", err)
			return
		}
		c.Set("session", session)
		if protocol != "" {
			c.Set(clientWebSocketProtocolContext, protocol)
		}
		c.Next()
	}
}

func clientBearerCredentials(request *http.Request) (string, string) {
	authorization := strings.TrimSpace(request.Header.Get("Authorization"))
	const prefix = "Bearer "
	if strings.HasPrefix(authorization, prefix) {
		return strings.TrimSpace(strings.TrimPrefix(authorization, prefix)), ""
	}
	for _, candidate := range strings.Split(request.Header.Get("Sec-WebSocket-Protocol"), ",") {
		protocol := strings.TrimSpace(candidate)
		if strings.HasPrefix(protocol, clientBearerWebSocketPrefix) {
			return strings.TrimPrefix(protocol, clientBearerWebSocketPrefix), protocol
		}
	}
	return "", ""
}
