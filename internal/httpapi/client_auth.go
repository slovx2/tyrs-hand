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
			// Web Live 页面沿用现有登录 Cookie；写请求仍必须通过 CSRF 校验。
			if strings.HasPrefix(c.Request.URL.Path, "/api/v1/client/live-") {
				cookie, cookieErr := c.Cookie(sessionCookie)
				if cookieErr == nil && cookie != "" {
					session, authErr := s.auth.Authenticate(c.Request.Context(), cookie)
					if authErr == nil {
						if c.Request.Method != http.MethodGet && !s.auth.ValidateCSRF(c.Request.Context(), cookie, c.GetHeader("X-CSRF-Token")) {
							problem(c, http.StatusForbidden, "CSRF 校验失败", nil)
							return
						}
						c.Set("session", session)
						c.Next()
						return
					}
				}
			}
			problem(c, http.StatusUnauthorized, "需要 Bearer Token", auth.ErrSessionInvalid)
			return
		}
		var session auth.Session
		var err error
		if strings.HasPrefix(token, "tdv1.") {
			session, err = s.authenticateClientDevice(c.Request.Context(), token)
			if deviceID, ok := parseClientDeviceToken(token); ok {
				c.Set(clientDeviceContext, deviceID)
			}
		} else {
			session, err = s.auth.Authenticate(c.Request.Context(), token)
		}
		if err != nil {
			if errors.Is(err, auth.ErrSessionInvalid) {
				problem(c, http.StatusUnauthorized, "客户端会话无效", err)
			} else {
				problem(c, http.StatusInternalServerError, "客户端认证失败", err)
			}
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
