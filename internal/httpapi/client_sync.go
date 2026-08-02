package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func (s *Server) clientSync(c *gin.Context) {
	after, err := strconv.ParseInt(c.DefaultQuery("afterCursor", "0"), 10, 64)
	if err != nil || after < 0 {
		badRequest(c, errors.New("afterCursor 无效"))
		return
	}
	limit := 500
	if value, parseErr := strconv.Atoi(c.Query("limit")); parseErr == nil &&
		value > 0 && value <= 500 {
		limit = value
	}
	var earliest sql.NullInt64
	var latest int64
	err = s.db.QueryRowContext(c.Request.Context(), `SELECT
		min(cursor) FILTER (WHERE created_at >= $1),COALESCE(max(cursor),0)
		FROM client_updates`, clientSyncRetentionStart()).Scan(&earliest, &latest)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取同步游标失败", err)
		return
	}
	if after > 0 && latest > after && earliest.Valid && after < earliest.Int64-1 {
		clientCursorResets.Inc()
		c.Header("Content-Type", "application/problem+json")
		c.AbortWithStatusJSON(http.StatusGone, Problem{Type: "about:blank",
			Title: "同步游标已过期", Status: http.StatusGone,
			Detail:   "请清空当前 Control 的业务缓存并重新执行 bootstrap",
			Instance: c.Request.URL.Path, RequestID: c.GetString("request_id"),
			ResetRequired: true})
		return
	}
	rows, err := s.db.QueryContext(c.Request.Context(), `SELECT cursor,session_id::text,
		update_type,COALESCE(entity_type,''),entity_id,entity_seq,entity_version,payload,
		created_at FROM client_updates WHERE cursor>$1 AND created_at >= $2
		ORDER BY cursor LIMIT $3`, after, clientSyncRetentionStart(), limit+1)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取同步事件失败", err)
		return
	}
	defer func() { _ = rows.Close() }()
	updates := make([]clientUpdate, 0, limit)
	for rows.Next() {
		var update clientUpdate
		var sessionID sql.NullString
		var entitySeq, entityVersion sql.NullInt64
		if err = rows.Scan(&update.Cursor, &sessionID, &update.Type, &update.EntityType,
			&update.EntityID, &entitySeq, &entityVersion, &update.Payload,
			&update.CreatedAt); err != nil {
			problem(c, http.StatusInternalServerError, "解析同步事件失败", err)
			return
		}
		update.Kind = "durable"
		if sessionID.Valid {
			parsed, parseErr := uuid.Parse(sessionID.String)
			if parseErr != nil {
				problem(c, http.StatusInternalServerError, "解析同步 Session 失败", parseErr)
				return
			}
			update.SessionID = &parsed
		}
		if entitySeq.Valid {
			update.EntitySeq = &entitySeq.Int64
		}
		if entityVersion.Valid {
			update.EntityVersion = &entityVersion.Int64
		}
		updates = append(updates, update)
	}
	if err = rows.Err(); err != nil {
		problem(c, http.StatusInternalServerError, "读取同步事件失败", err)
		return
	}
	hasMore := len(updates) > limit
	if hasMore {
		updates = updates[:limit]
	}
	next := after
	if len(updates) > 0 {
		next = updates[len(updates)-1].Cursor
	}
	c.JSON(http.StatusOK, gin.H{"updates": updates, "nextCursor": next,
		"hasMore": hasMore, "latestCursor": latest})
}
