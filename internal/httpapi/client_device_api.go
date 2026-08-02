package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type clientPushTokenRequest struct {
	Token          string `json:"token" binding:"required"`
	Platform       string `json:"platform" binding:"required"`
	AppEnvironment string `json:"appEnvironment" binding:"required"`
}

func clientRequestDeviceID(c *gin.Context) (uuid.UUID, bool) {
	value, exists := c.Get(clientDeviceContext)
	if !exists {
		problem(c, http.StatusForbidden, "此操作需要已绑定的设备凭证", nil)
		return uuid.Nil, false
	}
	id, ok := value.(uuid.UUID)
	if !ok || id == uuid.Nil {
		problem(c, http.StatusForbidden, "设备身份无效", nil)
		return uuid.Nil, false
	}
	return id, true
}

func (s *Server) clientPutPushToken(c *gin.Context) {
	deviceID, ok := clientRequestDeviceID(c)
	if !ok {
		return
	}
	var request clientPushTokenRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	request.Token = strings.TrimSpace(request.Token)
	if !strings.HasPrefix(request.Token, "ExponentPushToken[") &&
		!strings.HasPrefix(request.Token, "ExpoPushToken[") {
		badRequest(c, errors.New("Expo Push Token 无效"))
		return
	}
	if request.Platform != "ios" && request.Platform != "android" {
		badRequest(c, errors.New("平台无效"))
		return
	}
	if request.AppEnvironment != "development" && request.AppEnvironment != "preview" &&
		request.AppEnvironment != "production" {
		badRequest(c, errors.New("应用环境无效"))
		return
	}
	_, err := s.db.ExecContext(c.Request.Context(), `INSERT INTO client_push_tokens(
		device_id,expo_push_token,platform,app_environment) VALUES ($1,$2,$3,$4)
		ON CONFLICT(expo_push_token) DO UPDATE SET device_id=EXCLUDED.device_id,
		platform=EXCLUDED.platform,app_environment=EXCLUDED.app_environment,enabled=true,
		last_error=NULL,last_registered_at=now(),updated_at=now()`, deviceID,
		request.Token, request.Platform, request.AppEnvironment)
	if err != nil {
		problem(c, http.StatusInternalServerError, "注册 Push Token 失败", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) clientDeletePushToken(c *gin.Context) {
	deviceID, ok := clientRequestDeviceID(c)
	if !ok {
		return
	}
	token := strings.TrimSpace(c.Query("token"))
	if token == "" {
		badRequest(c, errors.New("token 不能为空"))
		return
	}
	_, err := s.db.ExecContext(c.Request.Context(), `DELETE FROM client_push_tokens
		WHERE device_id=$1 AND expo_push_token=$2`, deviceID, token)
	if err != nil {
		problem(c, http.StatusInternalServerError, "删除 Push Token 失败", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) clientDeleteDevice(c *gin.Context) {
	deviceID, ok := clientRequestDeviceID(c)
	if !ok {
		return
	}
	result, err := s.db.ExecContext(c.Request.Context(), `DELETE FROM client_devices WHERE id=$1`,
		deviceID)
	if err != nil {
		problem(c, http.StatusInternalServerError, "撤销设备失败", err)
		return
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		problem(c, http.StatusNotFound, "设备不存在", err)
		return
	}
	c.Status(http.StatusNoContent)
}
