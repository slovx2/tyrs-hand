package httpapi

import (
	"encoding/json"

	"github.com/gin-gonic/gin"
	"github.com/slovx2/tyrs-hand/internal/auth"
	"go.uber.org/zap"
)

func (s *Server) audit(c *gin.Context, action, resourceType, resourceID string, metadata map[string]any) {
	var administratorID any
	if value, ok := c.Get("session"); ok {
		administratorID = value.(auth.Session).AdministratorID
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		s.logger.Error("编码审计信息失败", zap.Error(err))
		return
	}
	_, err = s.db.ExecContext(c.Request.Context(), `
		INSERT INTO audit_logs(administrator_id, action, resource_type, resource_id, request_id, ip_address, metadata)
		VALUES ($1, $2, $3, NULLIF($4, ''), $5, NULLIF($6, '')::inet, $7)`,
		administratorID, action, resourceType, resourceID, c.GetString("request_id"), c.ClientIP(), raw)
	if err != nil {
		s.logger.Error("写入审计日志失败", zap.Error(err), zap.String("action", action))
	}
}
