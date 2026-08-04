package httpapi

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type clientTurnPageItem struct {
	Kind      string          `json:"kind"`
	ID        string          `json:"id"`
	AnchorSeq int64           `json:"anchorSeq"`
	Messages  []clientMessage `json:"messages"`
	Runs      []clientTurnRun `json:"runs"`
}

type clientTurnRun struct {
	ID                  uuid.UUID                   `json:"id"`
	Attempt             int                         `json:"attempt"`
	Status              string                      `json:"status"`
	ActualSettings      clientRunSettings           `json:"actualSettings"`
	StartedAt           time.Time                   `json:"startedAt"`
	FinishedAt          *time.Time                  `json:"finishedAt"`
	ErrorCode           *string                     `json:"errorCode"`
	ErrorMessage        *string                     `json:"errorMessage"`
	Segments            []clientRunSegment          `json:"segments"`
	PendingInteractives []clientInteractiveSnapshot `json:"pendingInteractives"`
}

type clientRunSegment struct {
	ID                   uuid.UUID  `json:"id"`
	Sequence             int64      `json:"sequence"`
	TriggerType          string     `json:"triggerType"`
	TriggerMessageID     *uuid.UUID `json:"triggerMessageId"`
	InteractiveRequestID *uuid.UUID `json:"interactiveRequestId"`
	StartEventSequence   int64      `json:"startEventSequence"`
	EndEventSequence     *int64     `json:"endEventSequence"`
	ActivityCount        int64      `json:"activityCount"`
}

type turnAnchor struct {
	Kind string
	ID   string
	Seq  int64
}

func encodeTurnCursor(sequence int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(sequence, 10)))
}

func decodeTurnCursor(value string) (int64, error) {
	if value == "" {
		return int64(^uint64(0) >> 1), nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(string(raw), 10, 64)
}

func (s *Server) clientListTurns(c *gin.Context) {
	sessionID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	limit := 20
	if raw, parseErr := strconv.Atoi(c.Query("limit")); parseErr == nil && raw > 0 && raw <= 50 {
		limit = raw
	}
	before, err := decodeTurnCursor(c.Query("beforeCursor"))
	if err != nil || before <= 0 {
		badRequest(c, errors.New("beforeCursor 无效"))
		return
	}
	anchors, more, err := s.loadTurnAnchors(c, sessionID, before, limit)
	if errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusNotFound, "Session 不存在", err)
		return
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取会话轮次失败", err)
		return
	}
	items := make([]clientTurnPageItem, 0, len(anchors))
	for _, anchor := range anchors {
		item, loadErr := s.loadTurnPageItem(c, sessionID, anchor)
		if loadErr != nil {
			problem(c, http.StatusInternalServerError, "读取完整轮次失败", loadErr)
			return
		}
		items = append(items, item)
	}
	next := ""
	if more && len(anchors) > 0 {
		next = encodeTurnCursor(anchors[0].Seq)
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "hasMoreBefore": more, "nextCursor": next})
}

func (s *Server) loadTurnAnchors(c *gin.Context, sessionID uuid.UUID, before int64,
	limit int,
) ([]turnAnchor, bool, error) {
	var exists bool
	if err := s.db.QueryRowContext(c, `SELECT EXISTS(SELECT 1 FROM workspace_sessions WHERE id=$1)`,
		sessionID).Scan(&exists); err != nil {
		return nil, false, err
	}
	if !exists {
		return nil, false, sql.ErrNoRows
	}
	rows, err := s.db.QueryContext(c, `WITH anchors AS (
		SELECT 'turn' AS kind,conversation_turn_id::text AS id,min(seq) AS anchor_seq
		FROM session_messages WHERE session_id=$1 AND conversation_turn_id IS NOT NULL
		GROUP BY conversation_turn_id
		UNION ALL
		SELECT 'message',id::text,seq FROM session_messages
		WHERE session_id=$1 AND conversation_turn_id IS NULL
	)
	SELECT kind,id,anchor_seq FROM anchors WHERE anchor_seq<$2
	ORDER BY anchor_seq DESC LIMIT $3`, sessionID, before, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()
	anchors := make([]turnAnchor, 0, limit+1)
	for rows.Next() {
		var item turnAnchor
		if err = rows.Scan(&item.Kind, &item.ID, &item.Seq); err != nil {
			return nil, false, err
		}
		anchors = append(anchors, item)
	}
	more := len(anchors) > limit
	if more {
		anchors = anchors[:limit]
	}
	for left, right := 0, len(anchors)-1; left < right; left, right = left+1, right-1 {
		anchors[left], anchors[right] = anchors[right], anchors[left]
	}
	return anchors, more, rows.Err()
}

func (s *Server) loadTurnPageItem(c *gin.Context, sessionID uuid.UUID,
	anchor turnAnchor,
) (clientTurnPageItem, error) {
	item := clientTurnPageItem{Kind: anchor.Kind, ID: anchor.ID, AnchorSeq: anchor.Seq,
		Messages: make([]clientMessage, 0), Runs: make([]clientTurnRun, 0)}
	var rows *sql.Rows
	var err error
	if anchor.Kind == "turn" {
		rows, err = s.db.QueryContext(c, `SELECT `+clientMessageColumns+`
			FROM session_messages WHERE session_id=$1 AND conversation_turn_id=$2
			ORDER BY seq`, sessionID, anchor.ID)
	} else {
		rows, err = s.db.QueryContext(c, `SELECT `+clientMessageColumns+`
			FROM session_messages WHERE session_id=$1 AND id=$2`, sessionID, anchor.ID)
	}
	if err != nil {
		return item, err
	}
	for rows.Next() {
		message, scanErr := scanClientMessage(rows)
		if scanErr != nil {
			_ = rows.Close()
			return item, scanErr
		}
		item.Messages = append(item.Messages, message)
	}
	if err = rows.Close(); err != nil {
		return item, err
	}
	if err = s.loadClientMessageAttachments(c, item.Messages); err != nil || anchor.Kind != "turn" {
		return item, err
	}
	turnID, err := uuid.Parse(anchor.ID)
	if err != nil {
		return item, err
	}
	item.Runs, err = s.loadClientTurnRuns(c, turnID)
	return item, err
}

func (s *Server) clientGetTurn(c *gin.Context) {
	sessionID, sessionErr := uuid.Parse(c.Param("id"))
	turnID, turnErr := uuid.Parse(c.Param("turnId"))
	if sessionErr != nil || turnErr != nil {
		badRequest(c, errors.New("轮次参数无效"))
		return
	}
	var anchor int64
	if err := s.db.QueryRowContext(c, `SELECT min(seq) FROM session_messages
		WHERE session_id=$1 AND conversation_turn_id=$2`, sessionID, turnID).Scan(&anchor); err != nil {
		problem(c, http.StatusNotFound, "轮次不存在", err)
		return
	}
	item, err := s.loadTurnPageItem(c, sessionID,
		turnAnchor{Kind: "turn", ID: turnID.String(), Seq: anchor})
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取轮次失败", err)
		return
	}
	c.JSON(http.StatusOK, item)
}
