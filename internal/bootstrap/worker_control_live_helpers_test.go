//go:build integration

package bootstrap

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/pquerna/otp/totp"
	"github.com/slovx2/tyrs-hand/internal/auth"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/security"
	"github.com/stretchr/testify/require"
)

type controlLiveIdentity struct{ username, password, secret string }

func (i *controlLiveIdentity) configure(t *testing.T, ctx context.Context, providerURL string) controlRuntimeFixtureOptions {
	t.Helper()
	return controlRuntimeFixtureOptions{Background: true, ConfigureServer: func(db *sql.DB, box *security.SecretBox, cfg *config.Config) *auth.Service {
		i.username, i.password = "live-"+uuid.NewString(), "live-test-password-only"
		key, err := totp.Generate(totp.GenerateOpts{Issuer: "tyrs-live-test", AccountName: i.username})
		require.NoError(t, err)
		i.secret = key.Secret()
		hash, err := security.HashPassword(i.password)
		require.NoError(t, err)
		nonce, ciphertext, err := box.Encrypt([]byte(i.secret), "administrator.totp")
		require.NoError(t, err)
		// 仅建立本专项独立登录身份；全部请求仍经过真实认证，不能复用已有管理员。
		_, err = db.ExecContext(ctx, `INSERT INTO administrators(username,password_hash,totp_secret_ciphertext,recovery_codes_hash,role,enabled)
			VALUES ($1,$2,$3,'[]','admin',true)`, i.username, hash, append(nonce, ciphertext...))
		require.NoError(t, err)
		cfg.ModelBaseURL, cfg.ModelAPIKey = providerURL, "live-provider-test-only"
		return auth.NewService(db, box, "unused-live-setup", "")
	}}
}

type controlLiveHTTP struct {
	base, token string
	client      http.Client
	calls       []map[string]any
}

func (c *controlLiveHTTP) request(t *testing.T, ctx context.Context, method, path string, input any, want int, output any) *http.Response {
	t.Helper()
	body, err := json.Marshal(input)
	require.NoError(t, err)
	request, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	response, err := c.client.Do(request)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	require.NoError(t, err)
	c.calls = append(c.calls, map[string]any{"method": method, "path": path, "status": response.StatusCode})
	require.Equal(t, want, response.StatusCode, "Live HTTP %s %s 未返回预期状态", method, path)
	if output != nil {
		require.NoError(t, json.Unmarshal(data, output))
	}
	return response
}

func (c *controlLiveHTTP) login(t *testing.T, ctx context.Context, identity controlLiveIdentity) {
	t.Helper()
	code, err := totp.GenerateCode(identity.secret, time.Now())
	require.NoError(t, err)
	response := c.request(t, ctx, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": identity.username, "password": identity.password, "totp": code}, http.StatusOK, nil)
	for _, cookie := range response.Cookies() {
		if cookie.Name == "tyrs_hand_session" {
			c.token = cookie.Value
		}
	}
	require.NotEmpty(t, c.token, "必须从真实登录响应取得会话，不能绕过认证")
}

type controlLiveProvider struct {
	server      *httptest.Server
	mu          sync.Mutex
	writeMu     sync.Mutex
	connection  *websocket.Conn
	created     int
	attachCount int
	attached    chan struct{}
	closed      chan struct{}
	events      []string
}

func newControlLiveProvider(t *testing.T) *controlLiveProvider {
	t.Helper()
	f := &controlLiveProvider{attached: make(chan struct{}), closed: make(chan struct{})}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer live-provider-test-only" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/v1/live/sessions" {
			var input struct {
				Transport struct{ Type, SDP string }
				Session   struct{ Model string }
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
			require.Equal(t, "webrtc", input.Transport.Type)
			require.True(t, strings.HasSuffix(input.Transport.SDP, "\r\n"))
			require.Equal(t, "live-loopback-model", input.Session.Model)
			f.mu.Lock()
			f.created++
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"session": map[string]string{"id": "live-loopback-session"},
				"transport": map[string]string{"type": "webrtc", "sdp": "v=0\r\nloopback-answer\r\n"}}))
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/v1/live/sessions/live-loopback-session/attach" {
			http.NotFound(w, r)
			return
		}
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		connection, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer func() { _ = connection.Close() }()
		f.mu.Lock()
		f.attachCount++
		if f.attachCount != 1 {
			f.mu.Unlock()
			t.Error("正常 Live 链路不应重复 attach")
			return
		}
		f.connection = connection
		f.mu.Unlock()
		f.send(t, map[string]any{"type": "session.started", "id": "live-started"})
		close(f.attached)
		for {
			var event map[string]any
			if err := connection.ReadJSON(&event); err != nil {
				return
			}
			kind, _ := event["type"].(string)
			f.mu.Lock()
			f.events = append(f.events, kind)
			f.mu.Unlock()
			if kind == "session.close" {
				f.send(t, map[string]any{"type": "session.closed", "id": "live-closed"})
				close(f.closed)
				return
			}
		}
	}))
	t.Cleanup(func() {
		f.mu.Lock()
		if f.connection != nil {
			_ = f.connection.Close()
		}
		f.mu.Unlock()
		f.server.Close()
	})
	return f
}

func (f *controlLiveProvider) send(t *testing.T, event map[string]any) {
	t.Helper()
	f.mu.Lock()
	connection := f.connection
	f.mu.Unlock()
	require.NotNil(t, connection)
	f.writeMu.Lock()
	defer f.writeMu.Unlock()
	require.NoError(t, connection.WriteJSON(event))
}

func (f *controlLiveProvider) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.created
}
