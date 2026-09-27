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
	"sync"
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

// 取消必须先让适配器侧回调连接断开，再让客户端看到回调终止；否则客户端据此释放的迟到 token 可能先被提交。
func TestOAuthCancellationClosesUpstreamBeforeClientStream(t *testing.T) {
	for _, reason := range []string{"stream", "owner", "hub"} {
		t.Run(reason, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			defer func() { _ = listener.Close() }()
			arrived := make(chan struct{})
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()
				buffer := make([]byte, 4096)
				if _, err := conn.Read(buffer); err == nil {
					close(arrived)
				}
				_, _ = io.Copy(io.Discard, conn)
			}()
			streamClosed, upstreamClosed := make(chan struct{}), make(chan struct{})
			violations := make(chan string, 1)
			original := dialOAuthCallback
			defer func() { dialOAuthCallback = original }()
			dialOAuthCallback = func(ctx context.Context, address string) (net.Conn, error) {
				conn, err := original(ctx, address)
				if err != nil {
					return nil, err
				}
				return &orderedCloseConn{Conn: conn, streamClosed: streamClosed, closed: upstreamClosed, violations: violations}, nil
			}
			port := uint32(listener.Addr().(*net.TCPAddr).Port)
			callback, err := url.Parse("http://127.0.0.1:" + strconv.FormatUint(uint64(port), 10) + "/callback")
			require.NoError(t, err)
			owner := newSession(1, RoleDesktop, nil, nil)
			hub := &Hub{sessions: map[int64]*session{1: owner}, done: make(chan struct{})}
			const state = "fixture-order-state-123456789"
			authorization := "https://authorization.example/authorize?" + url.Values{"state": {state}, "redirect_uri": {callback.String()}}.Encode()
			result, err := json.Marshal(map[string]string{"authorizationUrl": authorization})
			require.NoError(t, err)
			require.NoError(t, hub.registerOAuthCallback(owner, json.RawMessage(`{"name":"fixture"}`), result))
			client, server := net.Pipe()
			defer func() { _ = client.Close() }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- hub.ServeOAuthCallback(ctx, "127.0.0.1", port, server) }()
			_, err = io.WriteString(client, "GET /callback?state="+state+"&code=fixture HTTP/1.1\r\nHost: localhost\r\n\r\n")
			require.NoError(t, err)
			go func() { _, _ = io.Copy(io.Discard, client); close(streamClosed) }()
			select {
			case <-arrived:
			case <-time.After(time.Second):
				t.Fatal("真实 HTTP 回调未到达")
			}
			switch reason {
			case "stream":
				cancel()
			case "owner":
				owner.close(errSessionClosed)
			case "hub":
				require.NoError(t, hub.Close())
			}
			select {
			case <-streamClosed:
			case <-time.After(2 * time.Second):
				t.Fatal("取消后客户端回调通道未关闭")
			}
			select {
			case <-upstreamClosed:
			case <-time.After(2 * time.Second):
				t.Fatal("取消后适配器侧回调连接未关闭")
			}
			select {
			case violation := <-violations:
				t.Fatal(violation)
			default:
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

// orderedCloseConn 在首次关闭时给客户端通道留出抢先关闭的机会，用以确定性暴露错误顺序。
type orderedCloseConn struct {
	net.Conn
	once         sync.Once
	streamClosed <-chan struct{}
	closed       chan struct{}
	violations   chan<- string
}

func (c *orderedCloseConn) Close() error {
	c.once.Do(func() {
		select {
		case <-c.streamClosed:
			c.violations <- "客户端回调通道先于适配器侧连接关闭"
		case <-time.After(100 * time.Millisecond):
		}
		_ = c.Conn.Close()
		close(c.closed)
	})
	return nil
}
