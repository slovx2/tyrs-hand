package httpapi

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

type clientUpdate struct {
	Kind          string          `json:"kind"`
	Cursor        int64           `json:"cursor,omitempty"`
	SessionID     *uuid.UUID      `json:"sessionId"`
	Type          string          `json:"type"`
	EntityType    string          `json:"entityType,omitempty"`
	EntityID      string          `json:"entityId"`
	EntitySeq     *int64          `json:"entitySeq"`
	EntityVersion *int64          `json:"entityVersion,omitempty"`
	RunEventSeq   *int64          `json:"runEventSeq,omitempty"`
	Payload       json.RawMessage `json:"payload"`
	CreatedAt     time.Time       `json:"createdAt"`
}

type clientRPCNotification struct {
	Method string       `json:"method"`
	Params clientUpdate `json:"params"`
}

func (s *Server) clientUpdates(c *gin.Context) {
	cursor, err := strconv.ParseInt(c.DefaultQuery("cursor", "0"), 10, 64)
	if err != nil || cursor < 0 {
		badRequest(c, err)
		return
	}
	upgrader := websocket.Upgrader{
		HandshakeTimeout: 5 * time.Second,
		CheckOrigin: func(request *http.Request) bool {
			origin := request.Header.Get("Origin")
			if origin == "" {
				return true
			}
			publicURL, parseErr := url.Parse(s.cfg.PublicURL)
			return parseErr == nil && origin == publicURL.Scheme+"://"+publicURL.Host
		},
	}
	if protocol, exists := c.Get(clientWebSocketProtocolContext); exists {
		upgrader.Subprotocols = []string{protocol.(string)}
	}
	connection, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	clientSyncConnections.Inc()
	defer clientSyncConnections.Dec()
	defer func() { _ = connection.Close() }()
	var live <-chan clientUpdate
	cancelSubscription := func() {}
	if s.clientUpdateHub != nil {
		live, cancelSubscription = s.clientUpdateHub.subscribe()
	}
	defer cancelSubscription()
	connection.SetReadLimit(4096)
	_ = connection.SetReadDeadline(time.Now().Add(40 * time.Second))
	connection.SetPongHandler(func(string) error {
		return connection.SetReadDeadline(time.Now().Add(40 * time.Second))
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, readErr := connection.ReadMessage(); readErr != nil {
				return
			}
		}
	}()
	poll := time.NewTicker(500 * time.Millisecond)
	ping := time.NewTicker(25 * time.Second)
	defer poll.Stop()
	defer ping.Stop()
	for {
		next, sendErr := s.sendClientUpdates(c, connection, cursor)
		if sendErr != nil {
			return
		}
		cursor = next
		select {
		case <-c.Request.Context().Done():
			return
		case <-done:
			return
		case update, open := <-live:
			if !open {
				return
			}
			_ = connection.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err = connection.WriteJSON(clientRPCNotification{
				Method: "update", Params: update,
			}); err != nil {
				return
			}
		case <-poll.C:
		case <-ping.C:
			_ = connection.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err = connection.WriteControl(websocket.PingMessage, nil,
				time.Now().Add(5*time.Second)); err != nil {
				return
			}
		}
	}
}

func (s *Server) sendClientUpdates(c *gin.Context, connection *websocket.Conn,
	cursor int64,
) (int64, error) {
	rows, err := s.db.QueryContext(c.Request.Context(), `SELECT cursor,session_id::text,
		update_type,COALESCE(entity_type,''),entity_id,entity_seq,entity_version,payload,created_at
		FROM client_updates
		WHERE cursor>$1 ORDER BY cursor LIMIT 100`, cursor)
	if err != nil {
		return cursor, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var update clientUpdate
		var sessionID sql.NullString
		var entitySeq, entityVersion sql.NullInt64
		if err = rows.Scan(&update.Cursor, &sessionID, &update.Type, &update.EntityType,
			&update.EntityID, &entitySeq, &entityVersion, &update.Payload,
			&update.CreatedAt); err != nil {
			return cursor, err
		}
		if sessionID.Valid {
			parsed, parseErr := uuid.Parse(sessionID.String)
			if parseErr != nil {
				return cursor, parseErr
			}
			update.SessionID = &parsed
		}
		if entitySeq.Valid {
			update.EntitySeq = &entitySeq.Int64
		}
		if entityVersion.Valid {
			update.EntityVersion = &entityVersion.Int64
		}
		update.Kind = "durable"
		_ = connection.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err = connection.WriteJSON(clientRPCNotification{Method: "update", Params: update}); err != nil {
			return cursor, err
		}
		cursor = update.Cursor
	}
	return cursor, rows.Err()
}
