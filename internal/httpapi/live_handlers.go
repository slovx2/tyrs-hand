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
	"go.uber.org/zap"
)

const (
	defaultLiveModel = "gpt-live-1-codex"
	defaultLiveVoice = "cove"
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
		request.Model = defaultLiveModel
	}
	if request.Voice == "" {
		request.Voice = defaultLiveVoice
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

type liveRecoverySession struct {
	ID     uuid.UUID
	Remote string
	Status string
}

func (s *Server) createLiveSession(c *gin.Context)  { s.createLiveSessionForConversation(c, false) }
func (s *Server) recoverLiveSession(c *gin.Context) { s.createLiveSessionForConversation(c, true) }

func (s *Server) createLiveSessionForConversation(c *gin.Context, recovering bool) {
	conversationID, ok := liveIDParam(c)
	if !ok {
		return
	}
	var request liveSessionRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	request.Platform = strings.TrimSpace(request.Platform)
	if strings.TrimSpace(request.OfferSDP) == "" || len(request.OfferSDP) > 4<<20 ||
		(request.Platform != "web" && request.Platform != "android") {
		badRequest(c, errors.New("SDP offer 或客户端平台无效"))
		return
	}
	administratorID := c.MustGet("session").(auth.Session).AdministratorID
	requestCtx := c.Request.Context()
	var model, voice, instructions string
	var history []live.InputMessage

	tx, err := s.db.BeginTx(requestCtx, nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "创建 Live session 失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	if err = tx.QueryRowContext(requestCtx, `SELECT model,voice,instructions FROM live_conversations
		WHERE id=$1 AND administrator_id=$2 FOR UPDATE`, conversationID, administratorID).
		Scan(&model, &voice, &instructions); errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusNotFound, "Live conversation 不存在", err)
		return
	} else if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Live conversation 失败", err)
		return
	}
	if recovering {
		history, err = s.liveHistory(requestCtx, conversationID)
		if err != nil {
			problem(c, http.StatusInternalServerError, "读取 Live 历史失败", err)
			return
		}
	}

	oldSessions := make([]liveRecoverySession, 0, 1)
	const activeStatuses = `('creating','active','sideband_disconnected','recovering','closing','close_timeout')`
	if recovering {
		rows, queryErr := tx.QueryContext(requestCtx, `SELECT id,remote_session_id,status FROM live_sessions
			WHERE conversation_id=$1 AND status IN `+activeStatuses+` FOR UPDATE`, conversationID)
		if queryErr != nil {
			problem(c, http.StatusInternalServerError, "读取旧 Live session 失败", queryErr)
			return
		}
		for rows.Next() {
			var item liveRecoverySession
			if queryErr := rows.Scan(&item.ID, &item.Remote, &item.Status); queryErr != nil {
				_ = rows.Close()
				problem(c, http.StatusInternalServerError, "读取旧 Live session 失败", queryErr)
				return
			}
			if item.Status == "creating" || item.Status == "recovering" || item.Status == "closing" || item.Status == "close_timeout" {
				_ = rows.Close()
				problem(c, http.StatusConflict, "该 conversation 已有 Live session 正在创建、恢复或关闭", nil)
				return
			}
			oldSessions = append(oldSessions, item)
		}
		if queryErr := rows.Close(); queryErr != nil {
			problem(c, http.StatusInternalServerError, "读取旧 Live session 失败", queryErr)
			return
		}
		if _, err = tx.ExecContext(requestCtx, `UPDATE live_sessions SET status='replaced',updated_at=now()
			WHERE conversation_id=$1 AND status IN ('creating','active','sideband_disconnected','recovering')`, conversationID); err != nil {
			problem(c, http.StatusInternalServerError, "标记旧 Live session 失败", err)
			return
		}
	} else {
		var exists bool
		if err = tx.QueryRowContext(requestCtx, `SELECT EXISTS(SELECT 1 FROM live_sessions
			WHERE conversation_id=$1 AND status IN `+activeStatuses+`)`, conversationID).Scan(&exists); err != nil {
			problem(c, http.StatusInternalServerError, "检查 Live session 失败", err)
			return
		}
		if exists {
			problem(c, http.StatusConflict, "该 conversation 已有活动 Live session", nil)
			return
		}
	}

	localID := uuid.New()
	if _, err = tx.ExecContext(requestCtx, `INSERT INTO live_sessions
		(id,conversation_id,remote_session_id,client_platform,status) VALUES($1,$2,'',$3,'creating')`,
		localID, conversationID, request.Platform); err != nil {
		problem(c, http.StatusConflict, "该 conversation 已有活动 Live session", err)
		return
	}
	conversationStatus := "creating"
	if recovering {
		conversationStatus = "recovering"
	}
	if _, err = tx.ExecContext(requestCtx, `UPDATE live_conversations SET active_session_id=$1,status=$2,updated_at=now()
		WHERE id=$3`, localID, conversationStatus, conversationID); err != nil {
		problem(c, http.StatusInternalServerError, "更新 Live conversation 失败", err)
		return
	}
	if err = tx.Commit(); err != nil {
		problem(c, http.StatusInternalServerError, "创建 Live session 失败", err)
		return
	}

	for _, old := range oldSessions {
		s.liveManager.stopSession(old.ID)
	}
	result, err := s.liveManager.provider.CreateSession(requestCtx, request.OfferSDP, live.SessionConfig{
		Model: model, Voice: voice, Instructions: instructions,
	})
	serviceCtx := s.liveServiceContext(requestCtx)
	if err != nil {
		if recovering {
			s.restoreLiveRecoveryOrLog(serviceCtx, conversationID, localID, oldSessions, err)
		} else {
			s.failLiveSession(serviceCtx, localID, err)
		}
		problem(c, http.StatusBadGateway, "Live Provider 创建 session 失败", err)
		return
	}
	if _, err = s.db.ExecContext(serviceCtx, `UPDATE live_sessions SET remote_session_id=$2,status='recovering',updated_at=now() WHERE id=$1`, localID, result.ProviderSessionID); err != nil {
		if recovering {
			s.restoreLiveRecoveryOrLog(serviceCtx, conversationID, localID, oldSessions, err)
		} else {
			s.failLiveSession(serviceCtx, localID, err)
		}
		problem(c, http.StatusInternalServerError, "保存 Live session 失败", err)
		return
	}
	if err = s.liveManager.attach(requestCtx, localID, result.ProviderSessionID); err != nil {
		if recovering {
			s.restoreLiveRecoveryOrLog(serviceCtx, conversationID, localID, oldSessions, err)
		} else {
			s.failLiveSession(serviceCtx, localID, err)
		}
		problem(c, http.StatusServiceUnavailable, "Live manager 尚未就绪", err)
		return
	}
	sidebandCtx, cancelSideband := context.WithTimeout(requestCtx, 10*time.Second)
	defer cancelSideband()
	if err = s.liveManager.waitSideband(sidebandCtx, localID); err != nil {
		if recovering {
			s.restoreLiveRecoveryOrLog(serviceCtx, conversationID, localID, oldSessions, err)
		} else {
			_ = s.liveManager.updateSessionStatus(serviceCtx, localID, "sideband_disconnected", err.Error())
		}
		problem(c, http.StatusBadGateway, "Live sideband 连接失败", err)
		return
	}
	if recovering && len(history) > 0 {
		if err = s.liveManager.waitEventType(sidebandCtx, localID, "session.started"); err != nil {
			s.restoreLiveRecoveryOrLog(serviceCtx, conversationID, localID, oldSessions, err)
			problem(c, http.StatusBadGateway, "Live session 未启动，无法注入历史", err)
			return
		}
		if err = s.liveManager.injectHistory(sidebandCtx, localID, history); err != nil {
			s.restoreLiveRecoveryOrLog(serviceCtx, conversationID, localID, oldSessions, err)
			problem(c, http.StatusBadGateway, "注入 Live 历史失败", err)
			return
		}
	}
	response := liveSessionResponse{ConversationID: conversationID, SessionID: localID}
	response.Transport.Type = "webrtc"
	response.Transport.AnswerSDP = result.AnswerSDP
	response.Session.Status = "starting"
	c.JSON(http.StatusCreated, response)
}

func (s *Server) liveServiceContext(fallback context.Context) context.Context {
	if s.liveManager != nil {
		if ctx, err := s.liveManager.rootContext(); err == nil {
			return ctx
		}
	}
	return fallback
}

func (s *Server) failLiveSession(ctx context.Context, id uuid.UUID, cause error) {
	_, _ = s.db.ExecContext(ctx, `UPDATE live_sessions SET status='failed',last_error=$2,updated_at=now() WHERE id=$1 AND status NOT IN ('closed','expired','replaced')`, id, cause.Error())
	_, _ = s.db.ExecContext(ctx, `UPDATE live_conversations SET active_session_id=NULL,status='active',updated_at=now() WHERE active_session_id=$1 AND EXISTS(SELECT 1 FROM live_sessions WHERE id=$1 AND status='failed')`, id)
	s.liveManager.stopSession(id)
}

func (s *Server) restoreLiveRecoveryOrLog(ctx context.Context, conversationID, newID uuid.UUID, old []liveRecoverySession, cause error) {
	if err := s.restoreLiveRecovery(ctx, conversationID, newID, old, cause); err != nil && s.logger != nil {
		s.logger.Error("恢复 Live session 状态失败", zap.Error(err))
	}
}

func (s *Server) restoreLiveRecovery(ctx context.Context, conversationID, newID uuid.UUID, old []liveRecoverySession, cause error) error {
	defer s.liveManager.stopSession(newID)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE live_sessions SET status='failed',last_error=$2,updated_at=now() WHERE id=$1`, newID, cause.Error()); err != nil {
		return err
	}
	var activeID uuid.NullUUID
	conversationStatus := "active"
	restored := make([]liveRecoverySession, 0, len(old))
	for _, item := range old {
		status := item.Status
		if status == "creating" && item.Remote == "" {
			if _, err := tx.ExecContext(ctx, `UPDATE live_sessions SET status='failed',last_error=$2,updated_at=now() WHERE id=$1`, item.ID, "恢复失败且旧 session 尚未获得 provider ID"); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE live_sessions SET status=$2,last_error=NULL,updated_at=now() WHERE id=$1`, item.ID, status); err != nil {
			return err
		}
		restored = append(restored, item)
		if status == "sideband_disconnected" {
			conversationStatus = status
		} else if conversationStatus != "sideband_disconnected" {
			conversationStatus = "active"
		}
		if !activeID.Valid {
			activeID = uuid.NullUUID{UUID: item.ID, Valid: true}
		}
	}
	if activeID.Valid {
		if _, err := tx.ExecContext(ctx, `UPDATE live_conversations SET active_session_id=$1,status=$2,updated_at=now() WHERE id=$3`, activeID.UUID, conversationStatus, conversationID); err != nil {
			return err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `UPDATE live_conversations SET active_session_id=NULL,status='active',updated_at=now() WHERE id=$1`, conversationID); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, item := range restored {
		if item.Remote != "" {
			if err := s.liveManager.attach(ctx, item.ID, item.Remote); err != nil && s.logger != nil {
				s.logger.Warn("恢复 Live sideband 启动失败", zap.String("sessionId", item.ID.String()), zap.Error(err))
			}
		}
	}
	return nil
}

func (s *Server) waitLiveSideband(ctx context.Context, id uuid.UUID) error {
	return s.liveManager.waitSideband(ctx, id)
}

func (s *Server) closeLiveSession(c *gin.Context) {
	id, ok := liveIDParam(c)
	if !ok {
		return
	}
	administratorID := c.MustGet("session").(auth.Session).AdministratorID
	requestCtx := c.Request.Context()
	tx, err := s.db.BeginTx(requestCtx, nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Live session 失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var status string
	if err = tx.QueryRowContext(requestCtx, `SELECT ls.status FROM live_sessions ls
		JOIN live_conversations lc ON lc.id=ls.conversation_id
		WHERE ls.id=$1 AND lc.administrator_id=$2 FOR UPDATE`, id, administratorID).Scan(&status); errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusNotFound, "Live session 不存在", err)
		return
	} else if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Live session 失败", err)
		return
	}
	if status == "closed" || status == "expired" || status == "failed" || status == "replaced" {
		c.JSON(http.StatusOK, gin.H{"sessionId": id, "status": status})
		return
	}
	// A concurrent caller that observes closing only waits for the first
	// close command. close_timeout is explicitly retryable.
	sendClose := status != "closing"
	if sendClose {
		if _, err = tx.ExecContext(requestCtx, `UPDATE live_sessions SET status='closing',last_error=NULL,updated_at=now() WHERE id=$1`, id); err != nil {
			problem(c, http.StatusInternalServerError, "更新 Live session 状态失败", err)
			return
		}
		if _, err = tx.ExecContext(requestCtx, `UPDATE live_conversations SET status='closing',updated_at=now() WHERE active_session_id=$1`, id); err != nil {
			problem(c, http.StatusInternalServerError, "更新 Live conversation 状态失败", err)
			return
		}
	}
	if err = tx.Commit(); err != nil {
		problem(c, http.StatusInternalServerError, "关闭 Live session 失败", err)
		return
	}

	closeCtx, cancel := context.WithTimeout(requestCtx, 10*time.Second)
	defer cancel()
	if sendClose {
		if err = s.liveManager.waitSideband(closeCtx, id); err != nil {
			serviceCtx := s.liveServiceContext(requestCtx)
			_ = s.liveManager.updateSessionStatus(serviceCtx, id, "close_timeout", "等待 Live sideband 超时")
			problem(c, http.StatusGatewayTimeout, "等待 Live sideband 超时", err)
			return
		}
		if err = s.liveManager.sendClose(closeCtx, id); err != nil {
			serviceCtx := s.liveServiceContext(requestCtx)
			_ = s.liveManager.updateSessionStatus(serviceCtx, id, "close_timeout", err.Error())
			problem(c, http.StatusConflict, "发送 Live close 失败", err)
			return
		}
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err = s.db.QueryRowContext(closeCtx, `SELECT status FROM live_sessions WHERE id=$1`, id).Scan(&status); err != nil {
			problem(c, http.StatusInternalServerError, "读取关闭状态失败", err)
			return
		}
		if status == "closed" {
			c.JSON(http.StatusOK, gin.H{"sessionId": id, "status": status})
			return
		}
		if status == "expired" || status == "failed" || status == "replaced" {
			problem(c, http.StatusConflict, "Live session 未收到 session.closed", errors.New(status))
			return
		}
		select {
		case <-closeCtx.Done():
			serviceCtx := s.liveServiceContext(requestCtx)
			_ = s.liveManager.updateSessionStatus(serviceCtx, id, "close_timeout", "等待 session.closed 超时")
			problem(c, http.StatusGatewayTimeout, "等待 Live session 关闭超时", closeCtx.Err())
			return
		case <-ticker.C:
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

type liveHistoryRow struct {
	Role string
	Text string
}

const (
	liveHistoryLimit  = 128
	liveHistoryBudget = 8192
)

func buildLiveHistory(rows []liveHistoryRow) []live.InputMessage {
	items := make([]live.InputMessage, 0, len(rows))
	costs := make([]int, 0, len(rows))
	for _, row := range rows {
		text := strings.TrimSpace(row.Text)
		if text == "" || (row.Role != "developer" && row.Role != "user" && row.Role != "assistant") {
			continue
		}
		contentType := "input_text"
		if row.Role == "assistant" {
			contentType = "output_text"
		}
		items = append(items, live.InputMessage{
			Type: "message", Role: row.Role,
			Content: []live.InputContent{{Type: contentType, Text: text}},
		})
		costs = append(costs, len([]rune(text))+4)
	}
	// 查询结果是倒序；先恢复时间顺序，再从最旧消息开始淘汰。
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
		costs[i], costs[j] = costs[j], costs[i]
	}
	total := 0
	for _, cost := range costs {
		total += cost
	}
	start := 0
	for total > liveHistoryBudget && start < len(items)-1 {
		total -= costs[start]
		start++
	}
	items = items[start:]
	if len(items) == 1 && total > liveHistoryBudget {
		maxRunes := liveHistoryBudget - 4
		if maxRunes < 1 {
			return nil
		}
		runes := []rune(items[0].Content[0].Text)
		if len(runes) > maxRunes {
			items[0].Content[0].Text = string(runes[:maxRunes])
		}
	}
	return items
}

func (s *Server) liveHistory(ctx context.Context, conversationID uuid.UUID) ([]live.InputMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT role,text FROM live_messages
		WHERE conversation_id=$1 ORDER BY sequence DESC LIMIT 128`, conversationID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	historyRows := make([]liveHistoryRow, 0, liveHistoryLimit)
	for rows.Next() {
		var row liveHistoryRow
		if err := rows.Scan(&row.Role, &row.Text); err != nil {
			return nil, err
		}
		historyRows = append(historyRows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return buildLiveHistory(historyRows), nil
}
