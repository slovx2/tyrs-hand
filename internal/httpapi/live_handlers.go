package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/auth"
	"github.com/slovx2/tyrs-hand/internal/live"
)

type liveConversationRequest struct {
	Model        string `json:"model"`
	Voice        string `json:"voice"`
	Instructions string `json:"instructions"`
}
type liveSessionRequest struct {
	OfferSDP string `json:"offerSdp"`
	Platform string `json:"platform"`
}
type liveConversationResponse struct {
	ID              uuid.UUID  `json:"id"`
	Model           string     `json:"model"`
	Voice           string     `json:"voice"`
	Instructions    string     `json:"instructions"`
	Status          string     `json:"status"`
	ActiveSessionID *uuid.UUID `json:"activeSessionId,omitempty"`
	ContextRevision int64      `json:"contextRevision"`
	LastError       string     `json:"lastError,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}
type liveSessionResponse struct {
	ConversationID uuid.UUID `json:"conversationId"`
	SessionID      uuid.UUID `json:"sessionId"`
	Transport      struct {
		Type      string `json:"type"`
		AnswerSDP string `json:"answerSdp"`
	} `json:"transport"`
	Session struct {
		Status string `json:"status"`
	} `json:"session"`
}
type liveMessageResponse struct {
	Sequence        int64      `json:"sequence"`
	Role            string     `json:"role"`
	Text            string     `json:"text"`
	SourceSessionID *uuid.UUID `json:"sourceSessionId,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
}
type liveEventResponse struct {
	ID        int64           `json:"id"`
	Direction string          `json:"direction"`
	Type      string          `json:"type"`
	EventID   string          `json:"eventId,omitempty"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"createdAt"`
}

func (s *Server) createLiveConversation(c *gin.Context) {
	administratorID := c.MustGet("session").(auth.Session).AdministratorID
	var request liveConversationRequest
	if err := c.ShouldBindJSON(&request); err != nil && !errors.Is(err, io.EOF) {
		badRequest(c, err)
		return
	}
	request.Model = strings.TrimSpace(request.Model)
	request.Voice = strings.TrimSpace(request.Voice)
	if request.Model == "" {
		request.Model = s.cfg.LiveModel
	}
	if request.Voice == "" {
		request.Voice = s.cfg.LiveVoice
	}
	if request.Model == "" || len(request.Model) > 128 || len(request.Voice) > 64 || len(request.Instructions) > 32768 {
		badRequest(c, errors.New("Live conversation 配置无效"))
		return
	}
	var item liveConversationResponse
	item.ID = uuid.New()
	item.Model = request.Model
	item.Voice = request.Voice
	item.Instructions = request.Instructions
	item.Status = "active"
	item.CreatedAt = time.Now().UTC()
	item.UpdatedAt = item.CreatedAt
	_, err := s.db.ExecContext(c.Request.Context(), `INSERT INTO live_conversations(id,administrator_id,model,voice,instructions,status,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$7)`, item.ID, administratorID, item.Model, item.Voice, item.Instructions, item.Status, item.CreatedAt)
	if err != nil {
		problem(c, http.StatusInternalServerError, "创建 Live conversation 失败", err)
		return
	}
	c.JSON(http.StatusCreated, item)
}

func (s *Server) getLiveConversation(c *gin.Context) {
	id, ok := liveIDParam(c)
	if !ok {
		return
	}
	item, err := s.loadLiveConversation(c.Request.Context(), id, c.MustGet("session").(auth.Session).AdministratorID)
	if errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusNotFound, "Live conversation 不存在", err)
		return
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Live conversation 失败", err)
		return
	}
	c.JSON(http.StatusOK, item)
}

func (s *Server) loadLiveConversation(ctx context.Context, id, administratorID uuid.UUID) (liveConversationResponse, error) {
	var item liveConversationResponse
	var active uuid.NullUUID
	err := s.db.QueryRowContext(ctx, `SELECT id,model,voice,instructions,status,active_session_id,context_revision,
		COALESCE((SELECT last_error FROM live_sessions ls WHERE ls.conversation_id=lc.id AND ls.last_error IS NOT NULL ORDER BY ls.updated_at DESC LIMIT 1),''),created_at,updated_at
		FROM live_conversations lc WHERE id=$1 AND administrator_id=$2`, id, administratorID).Scan(&item.ID, &item.Model, &item.Voice, &item.Instructions, &item.Status, &active, &item.ContextRevision, &item.LastError, &item.CreatedAt, &item.UpdatedAt)
	if active.Valid {
		value := active.UUID
		item.ActiveSessionID = &value
	}
	return item, err
}

func (s *Server) createLiveSession(c *gin.Context)  { s.createLiveSessionForConversation(c, false) }
func (s *Server) recoverLiveSession(c *gin.Context) { s.createLiveSessionForConversation(c, true) }

func (s *Server) createLiveSessionForConversation(c *gin.Context, recover bool) {
	conversationID, ok := liveIDParam(c)
	if !ok {
		return
	}
	var request liveSessionRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	request.OfferSDP = strings.TrimSpace(request.OfferSDP)
	request.Platform = strings.TrimSpace(request.Platform)
	if request.OfferSDP == "" || len(request.OfferSDP) > 4<<20 || (request.Platform != "web" && request.Platform != "android") {
		badRequest(c, errors.New("SDP offer 或客户端平台无效"))
		return
	}
	administratorID := c.MustGet("session").(auth.Session).AdministratorID
	var model, voice, instructions string
	var history []live.InputMessage
	var replacedSessionIDs []uuid.UUID
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "创建 Live session 失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	if err = tx.QueryRowContext(c.Request.Context(), `SELECT model,voice,instructions FROM live_conversations WHERE id=$1 AND administrator_id=$2 FOR UPDATE`, conversationID, administratorID).Scan(&model, &voice, &instructions); errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusNotFound, "Live conversation 不存在", err)
		return
	} else if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Live conversation 失败", err)
		return
	}
	if recover {
		history, err = s.liveHistory(c.Request.Context(), conversationID)
		if err != nil {
			problem(c, http.StatusInternalServerError, "读取 Live 历史失败", err)
			return
		}
		rows, queryErr := tx.QueryContext(c.Request.Context(), `SELECT id FROM live_sessions WHERE conversation_id=$1 AND status IN ('creating','active','sideband_disconnected','recovering')`, conversationID)
		if queryErr != nil {
			problem(c, http.StatusInternalServerError, "读取旧 Live session 失败", queryErr)
			return
		}
		for rows.Next() {
			var oldID uuid.UUID
			if queryErr := rows.Scan(&oldID); queryErr != nil {
				_ = rows.Close()
				problem(c, http.StatusInternalServerError, "读取旧 Live session 失败", queryErr)
				return
			}
			replacedSessionIDs = append(replacedSessionIDs, oldID)
		}
		if queryErr := rows.Close(); queryErr != nil {
			problem(c, http.StatusInternalServerError, "读取旧 Live session 失败", queryErr)
			return
		}
		if _, err = tx.ExecContext(c.Request.Context(), `UPDATE live_sessions SET status='expired',expired_at=COALESCE(expired_at,now()),updated_at=now() WHERE conversation_id=$1 AND status IN ('creating','active','sideband_disconnected','recovering')`, conversationID); err != nil {
			problem(c, http.StatusInternalServerError, "标记旧 Live session 失败", err)
			return
		}
	} else {
		var exists bool
		if err = tx.QueryRowContext(c.Request.Context(), `SELECT EXISTS(SELECT 1 FROM live_sessions WHERE conversation_id=$1 AND status IN ('creating','active','sideband_disconnected','recovering'))`, conversationID).Scan(&exists); err != nil {
			problem(c, http.StatusInternalServerError, "检查 Live session 失败", err)
			return
		}
		if exists {
			problem(c, http.StatusConflict, "该 conversation 已有活动 Live session", nil)
			return
		}
	}
	localID := uuid.New()
	if _, err = tx.ExecContext(c.Request.Context(), `INSERT INTO live_sessions(id,conversation_id,remote_session_id,client_platform,status) VALUES($1,$2,'',$3,'creating')`, localID, conversationID, request.Platform); err != nil {
		problem(c, http.StatusConflict, "该 conversation 已有活动 Live session", err)
		return
	}
	if _, err = tx.ExecContext(c.Request.Context(), `UPDATE live_conversations SET active_session_id=$1,status='active',updated_at=now() WHERE id=$2`, localID, conversationID); err != nil {
		problem(c, http.StatusInternalServerError, "更新 Live conversation 失败", err)
		return
	}
	if err = tx.Commit(); err != nil {
		problem(c, http.StatusInternalServerError, "创建 Live session 失败", err)
		return
	}
	for _, oldID := range replacedSessionIDs {
		s.liveManager.stopSession(oldID)
	}
	result, err := s.liveManager.provider.CreateSession(c.Request.Context(), request.OfferSDP, live.SessionConfig{Model: model, Voice: voice, Instructions: instructions, Input: history})
	if err != nil {
		_, _ = s.db.ExecContext(context.Background(), `UPDATE live_sessions SET status='failed',last_error=$2,updated_at=now() WHERE id=$1`, localID, err.Error())
		_, _ = s.db.ExecContext(context.Background(), `UPDATE live_conversations SET active_session_id=NULL,updated_at=now() WHERE active_session_id=$1`, localID)
		problem(c, http.StatusBadGateway, "Live Provider 创建 session 失败", err)
		return
	}
	if _, err = s.db.ExecContext(c.Request.Context(), `UPDATE live_sessions SET remote_session_id=$2,status='recovering',updated_at=now() WHERE id=$1`, localID, result.ProviderSessionID); err != nil {
		problem(c, http.StatusInternalServerError, "保存 Live session 失败", err)
		return
	}
	s.liveManager.attach(context.Background(), localID, result.ProviderSessionID)
	if err = s.waitLiveSideband(c.Request.Context(), localID); err != nil {
		_, _ = s.db.ExecContext(context.Background(), `UPDATE live_sessions SET status='sideband_disconnected',last_error=$2,updated_at=now() WHERE id=$1`, localID, err.Error())
		problem(c, http.StatusBadGateway, "Live sideband 连接失败", err)
		return
	}
	response := liveSessionResponse{ConversationID: conversationID, SessionID: localID}
	response.Transport.Type = "webrtc"
	response.Transport.AnswerSDP = result.AnswerSDP
	response.Session.Status = "starting"
	c.JSON(http.StatusCreated, response)
}

func (s *Server) waitLiveSideband(ctx context.Context, id uuid.UUID) error {
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, ok := s.liveManager.sideband(id); ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("sideband 连接超时")
		case <-ticker.C:
		}
	}
}

func (s *Server) closeLiveSession(c *gin.Context) {
	id, ok := liveIDParam(c)
	if !ok {
		return
	}
	administratorID := c.MustGet("session").(auth.Session).AdministratorID
	var status string
	err := s.db.QueryRowContext(c.Request.Context(), `SELECT ls.status FROM live_sessions ls JOIN live_conversations lc ON lc.id=ls.conversation_id WHERE ls.id=$1 AND lc.administrator_id=$2`, id, administratorID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusNotFound, "Live session 不存在", err)
		return
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Live session 失败", err)
		return
	}
	if status == "closed" || status == "expired" || status == "failed" {
		c.JSON(http.StatusOK, gin.H{"sessionId": id, "status": status})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	if err := s.liveManager.sendClose(ctx, id); err != nil {
		problem(c, http.StatusConflict, "Live sideband 不可用", err)
		return
	}
	for {
		if err := s.db.QueryRowContext(ctx, `SELECT status FROM live_sessions WHERE id=$1`, id).Scan(&status); err != nil {
			problem(c, http.StatusInternalServerError, "读取关闭状态失败", err)
			return
		}
		if status == "closed" {
			c.JSON(http.StatusOK, gin.H{"sessionId": id, "status": status})
			return
		}
		select {
		case <-ctx.Done():
			_, _ = s.db.ExecContext(context.Background(), `UPDATE live_sessions SET status='close_timeout',last_error='等待 session.closed 超时',updated_at=now() WHERE id=$1 AND status NOT IN ('closed','expired')`, id)
			problem(c, http.StatusGatewayTimeout, "等待 Live session 关闭超时", ctx.Err())
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (s *Server) listLiveMessages(c *gin.Context) {
	conversationID, ok := liveIDParam(c)
	if !ok {
		return
	}
	administratorID := c.MustGet("session").(auth.Session).AdministratorID
	if !s.liveConversationExists(c, conversationID, administratorID) {
		return
	}
	limit, cursor := livePage(c)
	rows, err := s.db.QueryContext(c.Request.Context(), `SELECT sequence,role,text,source_session_id,created_at FROM live_messages WHERE conversation_id=$1 AND ($2::bigint=0 OR sequence<$2) ORDER BY sequence DESC LIMIT $3`, conversationID, cursor, limit+1)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Live 消息失败", err)
		return
	}
	defer rows.Close()
	items := make([]liveMessageResponse, 0, limit)
	var next int64
	for rows.Next() {
		var item liveMessageResponse
		if err := rows.Scan(&item.Sequence, &item.Role, &item.Text, &item.SourceSessionID, &item.CreatedAt); err != nil {
			problem(c, 500, "读取 Live 消息失败", err)
			return
		}
		if len(items) < limit {
			items = append(items, item)
		} else {
			next = item.Sequence
		}
	}
	result := gin.H{"items": items}
	if next > 0 {
		result["nextCursor"] = strconv.FormatInt(next, 10)
	}
	c.JSON(http.StatusOK, result)
}

func (s *Server) listLiveEvents(c *gin.Context) {
	conversationID, ok := liveIDParam(c)
	if !ok {
		return
	}
	administratorID := c.MustGet("session").(auth.Session).AdministratorID
	if !s.liveConversationExists(c, conversationID, administratorID) {
		return
	}
	limit, cursor := livePage(c)
	rows, err := s.db.QueryContext(c.Request.Context(), `SELECT e.id,e.direction,e.event_type,e.event_id,e.payload,e.created_at FROM live_events e JOIN live_sessions ls ON ls.id=e.live_session_id WHERE ls.conversation_id=$1 AND ($2::bigint=0 OR e.id<$2) ORDER BY e.id DESC LIMIT $3`, conversationID, cursor, limit+1)
	if err != nil {
		problem(c, 500, "读取 Live 事件失败", err)
		return
	}
	defer rows.Close()
	items := make([]liveEventResponse, 0, limit)
	var next int64
	for rows.Next() {
		var item liveEventResponse
		if err := rows.Scan(&item.ID, &item.Direction, &item.Type, &item.EventID, &item.Payload, &item.CreatedAt); err != nil {
			problem(c, 500, "读取 Live 事件失败", err)
			return
		}
		if len(items) < limit {
			items = append(items, item)
		} else {
			next = item.ID
		}
	}
	result := gin.H{"items": items}
	if next > 0 {
		result["nextCursor"] = strconv.FormatInt(next, 10)
	}
	c.JSON(http.StatusOK, result)
}

func (s *Server) liveConversationExists(c *gin.Context, id, administratorID uuid.UUID) bool {
	var exists bool
	if err := s.db.QueryRowContext(c.Request.Context(), `SELECT EXISTS(SELECT 1 FROM live_conversations WHERE id=$1 AND administrator_id=$2)`, id, administratorID).Scan(&exists); err != nil {
		problem(c, 500, "读取 Live conversation 失败", err)
		return false
	}
	if !exists {
		problem(c, 404, "Live conversation 不存在", sql.ErrNoRows)
		return false
	}
	return true
}
func liveIDParam(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, errors.New("Live ID 无效"))
		return uuid.Nil, false
	}
	return id, true
}
func livePage(c *gin.Context) (int, int64) {
	limit := 50
	if raw := c.Query("limit"); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value > 0 {
			limit = value
		}
	}
	if limit > 200 {
		limit = 200
	}
	var cursor int64
	if raw := c.Query("cursor"); raw != "" {
		cursor, _ = strconv.ParseInt(raw, 10, 64)
	}
	return limit, cursor
}
func (s *Server) liveHistory(ctx context.Context, conversationID uuid.UUID) ([]live.InputMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT role,text FROM live_messages WHERE conversation_id=$1 ORDER BY sequence DESC LIMIT 128`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]live.InputMessage, 0, 128)
	budget := 8192 * 4
	for rows.Next() {
		var role, text string
		if err := rows.Scan(&role, &text); err != nil {
			return nil, err
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		cost := len([]rune(text))
		if cost > budget {
			continue
		}
		items = append(items, live.InputMessage{Type: "message", Role: role, Content: []live.InputContent{{Type: map[string]string{"assistant": "output_text"}[role], Text: text}}})
		budget -= cost
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}
	for i := range items {
		if items[i].Content[0].Type == "" {
			items[i].Content[0].Type = "input_text"
		}
	}
	return items, nil
}
