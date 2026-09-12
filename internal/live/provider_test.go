package live

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func TestHTTPProviderCreateSession(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/live/sessions", r.URL.Path)
		require.Equal(t, "Bearer secret", r.Header.Get("Authorization"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&requestBody))
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"session":{"id":"opaque-id"},"transport":{"type":"webrtc","sdp":"answer"}}`))
	}))
	defer server.Close()
	result, err := NewProvider(server.URL, "secret").CreateSession(context.Background(), "offer", SessionConfig{
		Model: "gpt-live-1", Voice: "marin", Instructions: "be brief",
		Input: []InputMessage{{Type: "message", Role: "user", Content: []InputContent{{Type: "input_text", Text: "hello"}}}},
	})
	require.NoError(t, err)
	require.Equal(t, "opaque-id", result.ProviderSessionID)
	require.Equal(t, "answer", result.AnswerSDP)
	require.Equal(t, "gpt-live-1", requestBody["session"].(map[string]any)["model"])
	session := requestBody["session"].(map[string]any)
	audio := session["audio"].(map[string]any)
	output := audio["output"].(map[string]any)
	require.Equal(t, "marin", output["voice"])
	require.Equal(t, "webrtc", requestBody["transport"].(map[string]any)["type"])
}

func TestHTTPProviderRejectsInvalidResponse(t *testing.T) {
	for _, response := range []string{`{"session":{},"transport":{"sdp":"answer"}}`, `{"session":{"id":"id"},"transport":{}}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(response))
		}))
		_, err := NewProvider(server.URL, "secret").CreateSession(context.Background(), "offer", SessionConfig{})
		require.Error(t, err)
		server.Close()
	}
}

func TestHTTPProviderAttachSideband(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/v1/live/sessions/opaque%2Fid/attach", r.URL.EscapedPath())
		require.Equal(t, "Bearer secret", r.Header.Get("Authorization"))
		connection, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer connection.Close()
		_ = connection.WriteJSON(map[string]string{"type": "session.started"})
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	sideband, err := NewProvider(server.URL, "secret").AttachSideband(ctx, "opaque/id")
	require.NoError(t, err)
	defer sideband.Close()
	var event map[string]any
	require.NoError(t, sideband.ReadJSON(ctx, &event))
	require.Equal(t, "session.started", event["type"])
}

func TestHTTPProviderDoesNotFallbackWhenUnconfigured(t *testing.T) {
	_, err := NewProvider("", "").CreateSession(context.Background(), "offer", SessionConfig{})
	require.ErrorIs(t, err, ErrNotConfigured)
	require.True(t, strings.Contains(err.Error(), "Live"))
}

func TestHTTPProviderMarksMissingSidebandAsExpired(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusGone)
	}))
	defer server.Close()

	provider := NewProvider(server.URL, "secret")
	_, err := provider.AttachSideband(context.Background(), "expired-session")

	require.ErrorIs(t, err, ErrSessionExpired)
}

func TestWebsocketSidebandRejectsBinaryFrames(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer connection.Close()
		require.NoError(t, connection.WriteMessage(websocket.BinaryMessage, []byte("audio")))
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	sideband, err := NewProvider(server.URL, "secret").AttachSideband(ctx, "opaque-id")
	require.NoError(t, err)
	defer sideband.Close()

	var event map[string]any
	require.ErrorIs(t, sideband.ReadJSON(ctx, &event), ErrSidebandBinary)
}

func TestHTTPProviderRejectsNonCreatedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"session":{"id":"id"},"transport":{"sdp":"answer"}}`))
	}))
	defer server.Close()

	_, err := NewProvider(server.URL, "secret").CreateSession(context.Background(), "offer", SessionConfig{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "HTTP 200")
}

func TestWebsocketSidebandRejectsInvalidJSON(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer connection.Close()
		require.NoError(t, connection.WriteMessage(websocket.TextMessage, []byte("not-json")))
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	sideband, err := NewProvider(server.URL, "secret").AttachSideband(ctx, "opaque-id")
	require.NoError(t, err)
	defer sideband.Close()

	var event map[string]any
	require.ErrorIs(t, sideband.ReadJSON(ctx, &event), ErrSidebandInvalidJSON)
}
