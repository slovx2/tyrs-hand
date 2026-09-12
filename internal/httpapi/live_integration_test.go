//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/pquerna/otp/totp"
	"github.com/slovx2/tyrs-hand/internal/auth"
	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/slovx2/tyrs-hand/internal/live"
	"github.com/slovx2/tyrs-hand/internal/security"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type fakeLiveCall struct {
	id           string
	session      map[string]any
	writeMu      sync.Mutex
	mu           sync.Mutex
	connections  []*websocket.Conn
	attachCount  int
	dropFirst    bool
	closeSignals chan struct{}
}

type fakeLiveServer struct {
	server *httptest.Server
	mu     sync.Mutex
	calls  []*fakeLiveCall
}

func newFakeLiveServer(t *testing.T) *fakeLiveServer {
	t.Helper()
	fake := &fakeLiveServer{}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer provider-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/v1/live/sessions" {
			var request struct {
				Session   map[string]any `json:"session"`
				Transport struct {
					Type string `json:"type"`
					SDP  string `json:"sdp"`
				} `json:"transport"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Transport.Type != "webrtc" || !strings.HasSuffix(request.Transport.SDP, "\r\n") {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			fake.mu.Lock()
			call := &fakeLiveCall{
				id: "remote-" + uuid.NewString(), session: request.Session,
				dropFirst: len(fake.calls) == 0, closeSignals: make(chan struct{}, 1),
			}
			fake.calls = append(fake.calls, call)
			fake.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"session":   map[string]string{"id": call.id},
				"transport": map[string]string{"type": "webrtc", "sdp": "v=0\\r\\nanswer\\r\\n"},
			})
			return
		}
		const prefix = "/v1/live/sessions/"
		if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, prefix) || !strings.HasSuffix(r.URL.Path, "/attach") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, prefix), "/attach")
		call := fake.call(id)
		if call == nil {
			w.WriteHeader(http.StatusGone)
			return
		}
		connection, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		call.mu.Lock()
		call.attachCount++
		attachNumber := call.attachCount
		call.connections = append(call.connections, connection)
		call.mu.Unlock()
		call.writeJSON(map[string]any{"type": "session.started", "id": "started-" + id})
		if call.dropFirst && attachNumber == 1 {
			_ = connection.Close()
			return
		}
		for {
			messageType, payload, readErr := connection.ReadMessage()
			if readErr != nil {
				return
			}
			if messageType != websocket.TextMessage {
				continue
			}
			var event map[string]any
			if json.Unmarshal(payload, &event) != nil {
				continue
			}
			if event["type"] == "session.close" {
				call.closeSignals <- struct{}{}
				call.writeJSON(map[string]any{"type": "session.closed", "id": "closed-" + id})
				_ = connection.Close()
				return
			}
		}
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func (fake *fakeLiveServer) call(id string) *fakeLiveCall {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	for _, call := range fake.calls {
		if call.id == id {
			return call
		}
	}
	return nil
}

func (fake *fakeLiveServer) latest() *fakeLiveCall {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.calls) == 0 {
		return nil
	}
	return fake.calls[len(fake.calls)-1]
}

func (fake *fakeLiveServer) waitCall(t *testing.T, index int) *fakeLiveCall {
	t.Helper()
	var call *fakeLiveCall
	require.Eventually(t, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		if len(fake.calls) <= index {
			return false
		}
		call = fake.calls[index]
		return true
	}, 5*time.Second, 20*time.Millisecond)
	return call
}

func (call *fakeLiveCall) waitAttach(t *testing.T, count int) {
	t.Helper()
	require.Eventually(t, func() bool {
		call.mu.Lock()
		defer call.mu.Unlock()
		return call.attachCount >= count
	}, 5*time.Second, 20*time.Millisecond)
}

func (call *fakeLiveCall) writeJSON(value any) {
	call.mu.Lock()
	if len(call.connections) == 0 {
		call.mu.Unlock()
		return
	}
	connection := call.connections[len(call.connections)-1]
	call.mu.Unlock()
	call.writeMu.Lock()
	defer call.writeMu.Unlock()
	_ = connection.WriteJSON(value)
}

func (call *fakeLiveCall) waitClosed(t *testing.T) {
	t.Helper()
	select {
	case <-call.closeSignals:
	case <-time.After(5 * time.Second):
		t.Fatal("Fake Live Server 未收到 session.close")
	}
}

func TestLiveControlWithFakeProvider(t *testing.T) {
	db := workerDatabase(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, database.Migrate(ctx, db))
	box, err := security.NewSecretBox(make([]byte, 32))
	require.NoError(t, err)
	authService := auth.NewService(db, box, "live-setup-token", "")
	setup, err := authService.Setup(ctx, "live-setup-token", "live-admin", "test-password-123")
	require.NoError(t, err)
	fake := newFakeLiveServer(t)
	manager := newLiveManager(db, live.NewProvider(fake.server.URL, "provider-secret"), zap.NewNop())
	server := &Server{db: db, auth: authService, logger: zap.NewNop(), liveManager: manager}
	router := liveIntegrationRouter(server)
	httpServer := httptest.NewServer(router)
	t.Cleanup(httpServer.Close)
	go func() { _ = manager.Start(ctx) }()
	require.Eventually(t, func() bool {
		_, err := manager.rootContext()
		return err == nil
	}, time.Second, 10*time.Millisecond)

	code, err := totp.GenerateCode(setup.TOTPSecret, time.Now())
	require.NoError(t, err)
	login := clientJSONRequest(t, http.MethodPost, httpServer.URL+"/api/v1/client/auth/login", "",
		map[string]any{"username": "live-admin", "password": "test-password-123", "totp": code})
	require.Equal(t, http.StatusOK, login.Code, login.Body.String())
	var loginBody struct {
		AccessToken string `json:"accessToken"`
	}
	require.NoError(t, json.Unmarshal(login.Body.Bytes(), &loginBody))
	require.NotEmpty(t, loginBody.AccessToken)

	conversation := clientJSONRequest(t, http.MethodPost, httpServer.URL+"/api/v1/client/live-conversations",
		loginBody.AccessToken, map[string]any{"model": "gpt-live-test", "voice": "marin"})
	require.Equal(t, http.StatusCreated, conversation.Code, conversation.Body.String())
	var conversationBody liveConversationResponse
	require.NoError(t, json.Unmarshal(conversation.Body.Bytes(), &conversationBody))

	created := clientJSONRequest(t, http.MethodPost, httpServer.URL+"/api/v1/client/live-conversations/"+
		conversationBody.ID.String()+"/sessions", loginBody.AccessToken,
		map[string]any{"offerSdp": "offer-sdp\r\n", "platform": "web"})
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	var createdBody liveSessionResponse
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &createdBody))
	require.Equal(t, "v=0\\r\\nanswer\\r\\n", createdBody.Transport.AnswerSDP)
	require.NotContains(t, created.Body.String(), "provider-secret")

	call := fake.waitCall(t, 0)
	call.waitAttach(t, 2) // 第一条连接由 Fake Server 主动断开，验证 manager 重连原 session。
	require.Equal(t, "gpt-live-test", call.session["model"])
	require.NotContains(t, call.session, "apiKey")
	call.writeJSON(map[string]any{"type": "input_transcript.delta", "id": "input-delta", "item_id": "item-1", "delta": "你好"})
	call.writeJSON(map[string]any{"type": "input_transcript.delta", "id": "input-delta", "item_id": "item-1", "delta": "你好"})
	call.writeJSON(map[string]any{"type": "input_transcript.done", "id": "input-done", "item_id": "item-1", "transcript": "你好"})
	call.writeJSON(map[string]any{"type": "output_transcript.delta", "id": "output-delta", "response_id": "response-1", "delta": "你好，我是 Live"})
	call.writeJSON(map[string]any{"type": "output_transcript.done", "id": "output-done", "response_id": "response-1", "text": "你好，我是 Live"})
	call.writeJSON(map[string]any{"type": "session.output_audio.delta", "id": "audio-1", "delta": "secret-audio-base64"})
	require.Eventually(t, func() bool {
		var count int
		return db.QueryRowContext(ctx, `SELECT count(*) FROM live_messages WHERE conversation_id=$1`, conversationBody.ID).Scan(&count) == nil && count == 2
	}, 5*time.Second, 20*time.Millisecond)
	events := clientJSONRequest(t, http.MethodGet, httpServer.URL+"/api/v1/client/live-conversations/"+
		conversationBody.ID.String()+"/events?limit=100", loginBody.AccessToken, nil)
	require.Equal(t, http.StatusOK, events.Code, events.Body.String())
	require.NotContains(t, events.Body.String(), "secret-audio-base64")

	closed := clientJSONRequest(t, http.MethodPost, httpServer.URL+"/api/v1/client/live-sessions/"+
		createdBody.SessionID.String()+"/close", loginBody.AccessToken, nil)
	require.Equal(t, http.StatusOK, closed.Code, closed.Body.String())
	require.Contains(t, closed.Body.String(), `"status":"closed"`)
	call.waitClosed(t)

	recovered := clientJSONRequest(t, http.MethodPost, httpServer.URL+"/api/v1/client/live-conversations/"+
		conversationBody.ID.String()+"/recover", loginBody.AccessToken,
		map[string]any{"offerSdp": "recover-offer\r\n", "platform": "web"})
	require.Equal(t, http.StatusCreated, recovered.Code, recovered.Body.String())
	var recoveredBody liveSessionResponse
	require.NoError(t, json.Unmarshal(recovered.Body.Bytes(), &recoveredBody))
	require.NotEqual(t, createdBody.SessionID, recoveredBody.SessionID)
	recoveredCall := fake.waitCall(t, 1)
	recoveredCall.waitAttach(t, 1)
	input, ok := recoveredCall.session["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 2)
	require.Equal(t, "user", input[0].(map[string]any)["role"])
	require.Equal(t, "assistant", input[1].(map[string]any)["role"])

	finalClose := clientJSONRequest(t, http.MethodPost, httpServer.URL+"/api/v1/client/live-sessions/"+
		recoveredBody.SessionID.String()+"/close", loginBody.AccessToken, nil)
	require.Equal(t, http.StatusOK, finalClose.Code, finalClose.Body.String())
}

func liveIntegrationRouter(server *Server) http.Handler {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/v1/client/auth/login", server.clientLogin)
	client := router.Group("/api/v1/client")
	client.Use(server.requireClientBearer())
	client.POST("/live-conversations", server.createLiveConversation)
	client.GET("/live-conversations/:id", server.getLiveConversation)
	client.POST("/live-conversations/:id/sessions", server.createLiveSession)
	client.POST("/live-conversations/:id/recover", server.recoverLiveSession)
	client.POST("/live-sessions/:id/close", server.closeLiveSession)
	client.GET("/live-conversations/:id/messages", server.listLiveMessages)
	client.GET("/live-conversations/:id/events", server.listLiveEvents)
	return router
}
