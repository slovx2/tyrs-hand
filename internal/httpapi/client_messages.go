package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/slovx2/tyrs-hand/internal/auth"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
)

type clientMessage struct {
	ID            uuid.UUID          `json:"id"`
	SessionID     uuid.UUID          `json:"sessionId"`
	Seq           int64              `json:"seq"`
	LocalID       string             `json:"localId"`
	ParticipantID *uuid.UUID         `json:"participantId"`
	Role          string             `json:"role"`
	Content       json.RawMessage    `json:"content"`
	CreatedAt     time.Time          `json:"createdAt"`
	UpdatedAt     time.Time          `json:"updatedAt"`
	Attachments   []clientAttachment `json:"attachments"`
}

func scanClientMessage(row rowScanner) (clientMessage, error) {
	var result clientMessage
	var participant sql.NullString
	err := row.Scan(&result.ID, &result.SessionID, &result.Seq, &result.LocalID,
		&participant, &result.Role, &result.Content, &result.CreatedAt, &result.UpdatedAt)
	if participant.Valid {
		value, parseErr := uuid.Parse(participant.String)
		if parseErr != nil {
			return clientMessage{}, parseErr
		}
		result.ParticipantID = &value
	}
	if err == nil {
		result.Content = normalizeClientMessageContent(result.Content)
	}
	return result, err
}

func normalizeClientMessageContent(raw json.RawMessage) json.RawMessage {
	var direct struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &direct) == nil && direct.Type != "" {
		return raw
	}
	var legacy struct {
		V struct {
			Content struct {
				Data struct {
					Message string `json:"message"`
				} `json:"data"`
			} `json:"content"`
		} `json:"v"`
	}
	if json.Unmarshal(raw, &legacy) == nil && legacy.V.Content.Data.Message != "" {
		encoded, _ := json.Marshal(gin.H{"type": "text", "text": legacy.V.Content.Data.Message})
		return encoded
	}
	encoded, _ := json.Marshal(gin.H{"type": "event", "event": "unknown",
		"detail": string(raw)})
	return encoded
}

const clientMessageColumns = `id,session_id,seq,local_id,participant_id::text,
	message_role,content,created_at,updated_at`

func (s *Server) clientListMessages(c *gin.Context) {
	sessionID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	limit := 100
	if value, parseErr := strconv.Atoi(c.Query("limit")); parseErr == nil && value > 0 && value <= 200 {
		limit = value
	}
	afterRaw, hasAfter := c.GetQuery("afterSeq")
	after, parseAfterErr := strconv.ParseInt(afterRaw, 10, 64)
	if hasAfter && (parseAfterErr != nil || after < 0) {
		badRequest(c, errors.New("afterSeq 无效"))
		return
	}
	before, _ := strconv.ParseInt(c.Query("beforeSeq"), 10, 64)
	var rows *sql.Rows
	if hasAfter {
		rows, err = s.db.QueryContext(c.Request.Context(), `SELECT `+clientMessageColumns+`
			FROM session_messages WHERE session_id=$1 AND seq>$2
			ORDER BY seq LIMIT $3`, sessionID, after, limit+1)
	} else {
		if before <= 0 {
			before = int64(^uint64(0) >> 1)
		}
		rows, err = s.db.QueryContext(c.Request.Context(), `SELECT `+clientMessageColumns+`
			FROM (SELECT * FROM session_messages WHERE session_id=$1 AND seq<$2
				ORDER BY seq DESC LIMIT $3) messages ORDER BY seq`, sessionID, before, limit+1)
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取消息失败", err)
		return
	}
	defer func() { _ = rows.Close() }()
	items := make([]clientMessage, 0, limit)
	for rows.Next() {
		item, scanErr := scanClientMessage(rows)
		if scanErr != nil {
			problem(c, http.StatusInternalServerError, "解析消息失败", scanErr)
			return
		}
		items = append(items, item)
	}
	hasMore := len(items) > limit
	if hasMore {
		if hasAfter {
			items = items[:limit]
		} else {
			items = items[1:]
		}
	}
	if err = s.loadClientMessageAttachments(c.Request.Context(), items); err != nil {
		problem(c, http.StatusInternalServerError, "读取消息附件失败", err)
		return
	}
	var lastSeq int64
	if err = s.db.QueryRowContext(c.Request.Context(), `SELECT last_message_seq
		FROM workspace_sessions WHERE id=$1`, sessionID).Scan(&lastSeq); errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusNotFound, "Session 不存在", err)
		return
	} else if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Session 失败", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"messages": items, "lastMessageSeq": lastSeq,
		"hasMoreBefore": !hasAfter && hasMore,
		"hasMoreAfter":  hasAfter && hasMore,
	})
}

type createClientMessageRequest struct {
	LocalID       string      `json:"localId" binding:"required"`
	Text          string      `json:"text" binding:"required"`
	Behavior      string      `json:"behavior"`
	AttachmentIDs []uuid.UUID `json:"attachmentIds"`
}

func (s *Server) clientCreateMessage(c *gin.Context) {
	sessionID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	var request createClientMessageRequest
	if err = c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	request.LocalID = strings.TrimSpace(request.LocalID)
	request.Text = strings.TrimSpace(request.Text)
	if request.LocalID == "" || len(request.LocalID) > 200 || request.Text == "" ||
		len(request.AttachmentIDs) > 10 {
		badRequest(c, errors.New("localId 或 text 无效"))
		return
	}
	if request.Behavior == "" {
		request.Behavior = "start_when_idle"
	}
	if request.Behavior != "start_when_idle" && request.Behavior != "steer_if_active" {
		badRequest(c, errors.New("behavior 无效"))
		return
	}
	administrator := c.MustGet("session").(auth.Session)
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "保存消息失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var lifecycle string
	if err = tx.QueryRowContext(c.Request.Context(), `SELECT lifecycle_state
		FROM workspace_sessions WHERE id=$1 FOR UPDATE`, sessionID).Scan(&lifecycle); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, sql.ErrNoRows) {
			status = http.StatusNotFound
		}
		problem(c, status, "Session 不存在", err)
		return
	}
	if lifecycle != "active" {
		problem(c, http.StatusConflict, "Session 当前不可写", codexcontrol.ErrControlArchived)
		return
	}
	existing, existingErr := scanClientMessage(tx.QueryRowContext(c.Request.Context(),
		`SELECT `+clientMessageColumns+` FROM session_messages
		WHERE session_id=$1 AND local_id=$2`, sessionID, request.LocalID))
	if existingErr == nil {
		if err = tx.Commit(); err != nil {
			problem(c, http.StatusInternalServerError, "读取幂等消息失败", err)
			return
		}
		existingItems := []clientMessage{existing}
		if err = s.loadClientMessageAttachments(c.Request.Context(), existingItems); err != nil {
			problem(c, http.StatusInternalServerError, "读取消息附件失败", err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": existingItems[0], "deduplicated": true})
		return
	}
	if !errors.Is(existingErr, sql.ErrNoRows) {
		problem(c, http.StatusInternalServerError, "检查消息幂等键失败", existingErr)
		return
	}
	if err = upsertClientParticipant(c, tx, administrator); err != nil {
		problem(c, http.StatusInternalServerError, "保存消息参与者失败", err)
		return
	}
	content, _ := json.Marshal(gin.H{"t": "plain", "v": gin.H{
		"role": "user", "content": gin.H{"type": "codex", "data": gin.H{
			"type": "message", "message": request.Text,
		}},
	}})
	var created clientMessage
	row := tx.QueryRowContext(c.Request.Context(), `WITH sequence AS (
		UPDATE workspace_sessions SET last_message_seq=last_message_seq+1,
			last_activity_at=now(),updated_at=now() WHERE id=$1 RETURNING last_message_seq)
		INSERT INTO session_messages(session_id,seq,local_id,participant_id,message_role,content)
		SELECT $1,last_message_seq,$2,$3,'user',$4 FROM sequence
		RETURNING `+clientMessageColumns, sessionID, request.LocalID,
		administrator.AdministratorID, content)
	created, err = scanClientMessage(row)
	if err != nil {
		problem(c, http.StatusInternalServerError, "保存消息失败", err)
		return
	}
	if len(request.AttachmentIDs) > 0 {
		deviceID, ok := clientRequestDeviceID(c)
		if !ok {
			return
		}
		if err = linkClientAttachmentsTx(c.Request.Context(), tx, sessionID, created.ID,
			deviceID, request.AttachmentIDs); err != nil {
			problem(c, http.StatusUnprocessableEntity, "关联附件失败", err)
			return
		}
	}
	repository := codexcontrol.NewRepository(s.db, s.cfg.LeaseDuration,
		s.cfg.CodexMaxSteersPerTurn, s.cfg.CodexReconcileMaxAttempts)
	intentID, inserted, err := repository.Enqueue(c.Request.Context(), tx,
		codexcontrol.EnqueueRequest{
			SourceType: codexcontrol.SourceWorkspace, SessionID: sessionID,
			InputSurface: "client", AgentProfileID: uuid.Nil,
			IdempotencyKey: "client:message:" + sessionID.String() + ":" + request.LocalID,
			Instruction:    request.Text, Behavior: request.Behavior, ReplyPolicy: "silent",
			ActorLogin: administrator.Username, ActorPermission: "owner",
			ActorParticipantID: administrator.AdministratorID,
			ActorDisplayName:   administrator.Username,
		})
	if err != nil || !inserted {
		if err == nil {
			err = errors.New("消息 Intent 幂等键冲突")
		}
		problem(c, http.StatusInternalServerError, "消息入队失败", err)
		return
	}
	if _, err = tx.ExecContext(c.Request.Context(), `UPDATE session_messages SET
		turn_intent_id=$2 WHERE id=$1`, created.ID, intentID); err != nil {
		problem(c, http.StatusInternalServerError, "关联消息 Intent 失败", err)
		return
	}
	payload, _ := json.Marshal(gin.H{"message": created, "intentId": intentID})
	_, err = tx.ExecContext(c.Request.Context(), `INSERT INTO client_updates(
		session_id,update_type,entity_type,entity_id,entity_seq,entity_version,payload)
		VALUES ($1,'message.created','message',$2,$3,$3,$4)`, sessionID, created.ID.String(), created.Seq, payload)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "提交消息失败", err)
		return
	}
	if s.redis != nil {
		_ = s.redis.Publish(c.Request.Context(), codexcontrol.WakeupChannel, "queued").Err()
	}
	createdItems := []clientMessage{created}
	if err = s.loadClientMessageAttachments(c.Request.Context(), createdItems); err != nil {
		problem(c, http.StatusInternalServerError, "读取消息附件失败", err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"message": createdItems[0], "intentId": intentID, "deduplicated": false,
	})
}

func (s *Server) loadClientMessageAttachments(ctx context.Context, messages []clientMessage) error {
	if len(messages) == 0 {
		return nil
	}
	ids := make([]string, 0, len(messages))
	indexes := make(map[uuid.UUID]int, len(messages))
	for index := range messages {
		messages[index].Attachments = make([]clientAttachment, 0)
		ids = append(ids, messages[index].ID.String())
		indexes[messages[index].ID] = index
	}
	rows, err := s.db.QueryContext(ctx, `SELECT link.message_id,`+clientAttachmentColumns+`
		FROM session_message_attachments link JOIN session_attachments attachment
		ON attachment.id=link.attachment_id WHERE link.message_id=ANY($1::uuid[])
		ORDER BY link.message_id,link.ordinal`, pq.Array(ids))
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var messageID uuid.UUID
		var attachment clientAttachment
		var sessionID sql.NullString
		if err = rows.Scan(&messageID, &attachment.ID, &sessionID, &attachment.Kind,
			&attachment.OriginalFilename, &attachment.MediaType, &attachment.SizeBytes,
			&attachment.SHA256, &attachment.Status, &attachment.CreatedAt); err != nil {
			return err
		}
		if sessionID.Valid {
			parsed, parseErr := uuid.Parse(sessionID.String)
			if parseErr != nil {
				return parseErr
			}
			attachment.SessionID = &parsed
		}
		if index, exists := indexes[messageID]; exists {
			messages[index].Attachments = append(messages[index].Attachments, attachment)
		}
	}
	return rows.Err()
}

func upsertClientParticipant(c *gin.Context, tx *sql.Tx, session auth.Session) error {
	_, err := tx.ExecContext(c.Request.Context(), `INSERT INTO participants(id,kind,display_name)
		VALUES ($1,'administrator',$2) ON CONFLICT(id) DO UPDATE SET
		display_name=EXCLUDED.display_name,updated_at=now()`, session.AdministratorID,
		session.Username)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(c.Request.Context(), `INSERT INTO participant_identities(
		participant_id,provider,external_key) VALUES ($1,'administrator',$2)
		ON CONFLICT(provider,external_key) DO NOTHING`, session.AdministratorID,
		session.AdministratorID.String())
	return err
}
