package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type legacyArchiveSession struct {
	ID                  uuid.UUID `json:"id"`
	WorkspaceID         uuid.UUID `json:"workspaceId"`
	ProjectID           uuid.UUID `json:"projectId"`
	Title               string    `json:"title"`
	ExternalThreadID    *string   `json:"externalThreadId"`
	HistoryCompleteness string    `json:"historyCompleteness"`
	CreatedAt           time.Time `json:"createdAt"`
	UpdatedAt           time.Time `json:"updatedAt"`
}

func (s *Server) clientListLegacyArchive(c *gin.Context) {
	rows, err := s.db.QueryContext(c.Request.Context(), `SELECT session.id,
		session.workspace_id,session.workspace_project_id,session.title,
		control.external_thread_id,session.history_completeness,
		session.created_at,session.updated_at
		FROM workspace_sessions session
		LEFT JOIN LATERAL (
			SELECT external_thread_id FROM codex_thread_controls
			WHERE session_id=session.id AND external_thread_id IS NOT NULL
			ORDER BY created_at DESC,id DESC LIMIT 1
		) control ON true
		WHERE session.lifecycle_state='archived'
		ORDER BY session.last_activity_at DESC,session.id DESC`)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 legacy archive 失败", err)
		return
	}
	defer func() { _ = rows.Close() }()
	items := make([]legacyArchiveSession, 0)
	for rows.Next() {
		item, scanErr := scanLegacyArchiveSession(rows)
		if scanErr != nil {
			problem(c, http.StatusInternalServerError, "读取 legacy archive 失败", scanErr)
			return
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		problem(c, http.StatusInternalServerError, "读取 legacy archive 失败", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"sessions": items})
}

func (s *Server) clientGetLegacyArchive(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, errors.New("legacy archive id 无效"))
		return
	}
	item, err := scanLegacyArchiveSession(s.db.QueryRowContext(c.Request.Context(),
		`SELECT session.id,session.workspace_id,session.workspace_project_id,
		session.title,control.external_thread_id,session.history_completeness,
		session.created_at,session.updated_at
		FROM workspace_sessions session
		LEFT JOIN LATERAL (
			SELECT external_thread_id FROM codex_thread_controls
			WHERE session_id=session.id AND external_thread_id IS NOT NULL
			ORDER BY created_at DESC,id DESC LIMIT 1
		) control ON true
		WHERE session.id=$1 AND session.lifecycle_state='archived'`, id))
	if errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusNotFound, "legacy archive 不存在", err)
		return
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 legacy archive 失败", err)
		return
	}
	rows, err := s.db.QueryContext(c.Request.Context(), `SELECT id,seq,message_role,
		content,created_at FROM session_messages WHERE session_id=$1 ORDER BY seq,id`, id)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 legacy archive 消息失败", err)
		return
	}
	defer func() { _ = rows.Close() }()
	messages := make([]gin.H, 0)
	for rows.Next() {
		var messageID uuid.UUID
		var sequence int64
		var role string
		var content json.RawMessage
		var createdAt time.Time
		if err = rows.Scan(&messageID, &sequence, &role, &content, &createdAt); err != nil {
			problem(c, http.StatusInternalServerError, "读取 legacy archive 消息失败", err)
			return
		}
		messages = append(messages, gin.H{"id": messageID, "seq": sequence,
			"role": role, "content": content, "createdAt": createdAt})
	}
	c.JSON(http.StatusOK, gin.H{"session": item, "messages": messages, "readOnly": true})
}

func scanLegacyArchiveSession(row rowScanner) (legacyArchiveSession, error) {
	var item legacyArchiveSession
	var externalThreadID sql.NullString
	err := row.Scan(&item.ID, &item.WorkspaceID, &item.ProjectID, &item.Title,
		&externalThreadID, &item.HistoryCompleteness, &item.CreatedAt, &item.UpdatedAt)
	if externalThreadID.Valid {
		item.ExternalThreadID = &externalThreadID.String
	}
	return item, err
}
