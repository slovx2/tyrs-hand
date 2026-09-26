package appserverhub

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOAuthInFlightCallbackStopsWithOwnerOrHub(t *testing.T) {
	for _, reason := range []string{"owner", "hub"} {
		t.Run(reason, func(t *testing.T) {
			arrived, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			target := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				close(arrived)
				select {
				case <-r.Context().Done():
					close(canceled)
				case <-release:
				}
			}))
			defer target.Close()
			defer close(release)
			callback, err := url.Parse(target.URL + "/callback")
			require.NoError(t, err)
			port, err := strconv.ParseUint(callback.Port(), 10, 32)
			require.NoError(t, err)
			owner := newSession(1, RoleDesktop, nil, nil)
			hub := &Hub{sessions: map[int64]*session{1: owner}, done: make(chan struct{})}
			const state = "fixture-cancel-state-123456789"
			authorization := "https://authorization.example/authorize?" + url.Values{"state": {state}, "redirect_uri": {callback.String()}}.Encode()
			result, err := json.Marshal(map[string]string{"authorizationUrl": authorization})
			require.NoError(t, err)
			require.NoError(t, hub.registerOAuthCallback(owner, json.RawMessage(`{"name":"fixture"}`), result))
			client, server := net.Pipe()
			defer func() { _ = client.Close() }()
			done := make(chan error, 1)
			go func() { done <- hub.ServeOAuthCallback(context.Background(), "127.0.0.1", uint32(port), server) }()
			_, err = io.WriteString(client, "GET /callback?state="+state+"&code=fixture HTTP/1.1\r\nHost: localhost\r\n\r\n")
			require.NoError(t, err)
			select {
			case <-arrived:
			case <-time.After(time.Second):
				t.Fatal("真实 HTTP 回调未到达")
			}
			if reason == "owner" {
				owner.close(errSessionClosed)
			} else {
				require.NoError(t, hub.Close())
			}
			select {
			case <-canceled:
			case <-time.After(time.Second):
				t.Fatal("关闭所属连接后在途 OAuth HTTP 请求仍未取消")
			}
			select {
			case err := <-done:
				require.Error(t, err)
			case <-time.After(time.Second):
				t.Fatal("OAuth 转发未随取消退出")
			}
		})
	}
}
