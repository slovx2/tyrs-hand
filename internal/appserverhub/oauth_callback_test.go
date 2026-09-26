package appserverhub

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOAuthCallbackOnlyForwardsRegisteredHTTPFlow(t *testing.T) {
	const state = "fixture-state-has-more-than-16-random-bytes"
	var effects atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/callback", r.URL.Path)
		require.Equal(t, state, r.URL.Query().Get("state"))
		require.Equal(t, "fixture-code", r.URL.Query().Get("code"))
		require.Empty(t, r.Header.Get("Authorization"), "客户端任意凭据头不得转发")
		effects.Add(1)
		_, _ = io.WriteString(w, "authorized")
	}))
	defer target.Close()
	callback, err := url.Parse(target.URL + "/callback")
	require.NoError(t, err)
	port, err := strconv.ParseUint(callback.Port(), 10, 32)
	require.NoError(t, err)
	owner := newSession(1, RoleDesktop, nil, nil)
	hub := &Hub{sessions: map[int64]*session{1: owner}}
	authorization := url.URL{Scheme: "https", Host: "authorization.example", Path: "/authorize", RawQuery: url.Values{"state": []string{state}, "redirect_uri": []string{callback.String()}}.Encode()}
	result, err := json.Marshal(map[string]string{"authorizationUrl": authorization.String()})
	require.NoError(t, err)
	params, _ := json.Marshal(map[string]any{"name": "fixture", "timeoutSecs": 30})
	require.NoError(t, hub.registerOAuthCallback(owner, params, result))
	require.True(t, hub.OAuthCallbackAllowed("127.0.0.1", uint32(port)))
	require.False(t, hub.OAuthCallbackAllowed("10.0.0.1", uint32(port)))
	require.False(t, hub.OAuthCallbackAllowed("127.0.0.1", 22))
	send := func(request string) (int, error) {
		client, server := net.Pipe()
		defer func() { _ = client.Close() }()
		done := make(chan error, 1)
		go func() { done <- hub.ServeOAuthCallback(context.Background(), "127.0.0.1", uint32(port), server) }()
		_, err := io.WriteString(client, request)
		require.NoError(t, err)
		response, err := http.ReadResponse(bufio.NewReader(client), nil)
		require.NoError(t, err)
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		return response.StatusCode, <-done
	}
	query := url.Values{"state": []string{state}, "code": []string{"fixture-code"}}.Encode()
	for _, request := range []string{
		"GET /callback?state=wrong&code=fixture-code HTTP/1.1\r\nHost: localhost\r\n\r\n",
		"GET /other?" + query + " HTTP/1.1\r\nHost: localhost\r\n\r\n",
		"POST /callback?" + query + " HTTP/1.1\r\nHost: localhost\r\nContent-Length: 0\r\n\r\n",
		"GET http://external.example/callback?" + query + " HTTP/1.1\r\nHost: external.example\r\n\r\n",
	} {
		status, err := send(request)
		require.Equal(t, 403, status)
		require.Error(t, err)
		require.Zero(t, effects.Load())
		require.True(t, hub.OAuthCallbackAllowed("127.0.0.1", uint32(port)))
	}
	status, err := send("GET /callback?" + query + " HTTP/1.1\r\nHost: localhost\r\nAuthorization: must-not-forward\r\n\r\n")
	require.NoError(t, err)
	require.Equal(t, 200, status)
	require.Equal(t, int64(1), effects.Load())
	require.False(t, hub.OAuthCallbackAllowed("127.0.0.1", uint32(port)))
	status, err = send("GET /callback?" + query + " HTTP/1.1\r\nHost: localhost\r\n\r\n")
	require.Equal(t, 403, status)
	require.Error(t, err)
	require.Equal(t, int64(1), effects.Load())
}

func TestOAuthCallbackRejectsExpiredDisconnectedAndInvalidFlows(t *testing.T) {
	owner := newSession(1, RoleDesktop, nil, nil)
	hub := &Hub{sessions: map[int64]*session{1: owner}}
	register := func(callback, state string) error {
		authorization := "https://authorization.example/authorize?" + url.Values{"state": []string{state}, "redirect_uri": []string{callback}}.Encode()
		result, _ := json.Marshal(map[string]string{"authorizationUrl": authorization})
		params, _ := json.Marshal(map[string]any{"name": "fixture", "timeoutSecs": 30})
		return hub.registerOAuthCallback(owner, params, result)
	}
	state := "valid-long-single-use-state-123456"
	for _, callback := range []string{"http://10.0.0.1:1234/callback", "http://127.0.0.1:0/callback", "http://user:secret@127.0.0.1:1234/callback", "http://127.0.0.1:1234/callback?other=true"} {
		require.Error(t, register(callback, state))
		require.False(t, hub.OAuthCallbackAllowed("127.0.0.1", 1234))
	}
	require.Error(t, register("http://127.0.0.1:1234/callback", "short"))
	for _, reason := range []string{"expired", "disconnected", "closed"} {
		t.Run(reason, func(t *testing.T) {
			hub.closed = false
			hub.sessions[1] = owner
			require.NoError(t, register("http://127.0.0.1:1234/callback", state))
			switch reason {
			case "expired":
				flow := hub.oauthCallbacks[state]
				flow.expires = time.Now().Add(-time.Second)
				hub.oauthCallbacks[state] = flow
			case "disconnected":
				delete(hub.sessions, 1)
			case "closed":
				hub.closed = true
			}
			require.False(t, hub.OAuthCallbackAllowed("127.0.0.1", 1234), fmt.Sprintf("%s 必须撤销权限", reason))
		})
	}
}
