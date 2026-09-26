package appserverhub

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/stretchr/testify/require"
)

type reviewUpstream struct {
	ws       *websocket.Conn
	writes   sync.Mutex
	requests chan rpcMessage
}

func reviewRoutingFixture(t *testing.T, timeout time.Duration) (*Hub, *reviewUpstream) {
	t.Helper()
	directory, err := os.MkdirTemp("", "hub-review-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	path := filepath.Join(directory, "upstream.sock")
	listener, err := net.Listen("unix", path)
	require.NoError(t, err)
	fixture := &reviewUpstream{requests: make(chan rpcMessage, 32)}
	connected := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, upgradeErr := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if upgradeErr != nil {
			return
		}
		fixture.ws = ws
		close(connected)
		defer func() { _ = ws.Close() }()
		for {
			var message rpcMessage
			if ws.ReadJSON(&message) != nil {
				return
			}
			if message.Method == "initialize" {
				fixture.send(t, rpcMessage{ID: message.ID, Result: json.RawMessage(`{}`)})
			} else if message.Method != "initialized" {
				fixture.requests <- message
			}
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	hub, err := Start(context.Background(), Options{UpstreamSocketPath: path,
		RequestTimeout: timeout, EventBacklog: 64})
	require.NoError(t, err)
	<-connected
	t.Cleanup(func() { _ = hub.Close() })
	return hub, fixture
}

func (f *reviewUpstream) send(t *testing.T, message rpcMessage) {
	t.Helper()
	f.writes.Lock()
	defer f.writes.Unlock()
	require.NoError(t, f.ws.WriteJSON(message))
}

func reviewReceive[T any](t *testing.T, channel <-chan T) T {
	t.Helper()
	select {
	case value := <-channel:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("等待审查路由结果超时")
		var zero T
		return zero
	}
}

func reviewClient(t *testing.T, hub *Hub, calls *atomic.Int32) *Client {
	t.Helper()
	client, err := hub.OpenClient(ClientOptions{Role: RoleDesktop, DesktopTools: true,
		ServerRequestHandler: func(context.Context, codex.ServerRequest) (any, error) {
			calls.Add(1)
			return codex.TextToolResult("审查工具真实归属端", true), nil
		}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestReviewRoutingEarlyEventsAndToolOwner(t *testing.T) {
	for _, delivery := range []string{"inline", "detached"} {
		t.Run(delivery, func(t *testing.T) {
			hub, upstream := reviewRoutingFixture(t, time.Second)
			var observerCalls, ownerCalls atomic.Int32
			observer := reviewClient(t, hub, &observerCalls)
			owner := reviewClient(t, hub, &ownerCalls)
			owner.session.subscribe("parent")
			owner.session.subscribe("unrelated")
			events := owner.Events()
			threadID := "parent"
			if delivery == "detached" {
				threadID = "review-child"
			}
			completed := make(chan error, 1)
			go func() {
				var result json.RawMessage
				completed <- owner.Call(context.Background(), "review/start", map[string]any{
					"threadId": "parent", "delivery": delivery,
					"target": map[string]string{"type": "uncommittedChanges"},
				}, &result)
			}()
			request := reviewReceive(t, upstream.requests)
			require.Equal(t, "review/start", request.Method)
			for _, method := range []string{"thread/started", "turn/started", "item/started", "item/completed"} {
				upstream.send(t, rpcMessage{Method: method, Params: toolTestJSON(map[string]any{
					"threadId": threadID, "thread": map[string]string{"id": threadID},
					"turnId": "review-turn", "turn": map[string]string{"id": "review-turn"},
					"item": map[string]string{"id": "entered", "type": "enteredReviewMode"},
				})})
			}
			upstream.send(t, rpcMessage{ID: json.RawMessage(`"tool"`), Method: "item/tool/call",
				Params: toolTestJSON(map[string]any{"threadId": threadID, "turnId": "review-turn",
					"callId": "tool", "tool": "review_tool", "arguments": map[string]any{}})})
			// 已知无关会话仍能分发，也证明 reader 没被前面的通知/工具请求锁住。
			upstream.send(t, rpcMessage{Method: "item/completed", Params: json.RawMessage(`{"threadId":"unrelated"}`)})
			require.JSONEq(t, `{"threadId":"unrelated"}`, string(reviewReceive(t, events).Params))
			require.Zero(t, observerCalls.Load())
			require.Zero(t, ownerCalls.Load(), "响应前不能猜测工具执行端")
			upstream.send(t, rpcMessage{ID: request.ID, Result: toolTestJSON(map[string]any{
				"reviewThreadId": threadID, "turn": map[string]string{"id": "review-turn"},
			})})
			require.NoError(t, reviewReceive(t, completed))
			for _, method := range []string{"thread/started", "turn/started", "item/started", "item/completed"} {
				require.Equal(t, method, reviewReceive(t, events).Method)
			}
			toolAnswer := reviewReceive(t, upstream.requests)
			require.JSONEq(t, `"tool"`, string(toolAnswer.ID))
			require.Nil(t, toolAnswer.Error)
			require.Equal(t, int32(1), ownerCalls.Load())
			require.Zero(t, observerCalls.Load())
			if delivery == "detached" {
				require.True(t, observer.session.subscribed(threadID))
			}
			for _, method := range []string{"item/started", "item/completed", "turn/completed"} {
				upstream.send(t, rpcMessage{Method: method, Params: toolTestJSON(map[string]any{
					"threadId": threadID, "turnId": "review-turn", "turn": map[string]string{"id": "review-turn"},
					"item": map[string]string{"id": "exited", "type": "exitedReviewMode"},
				})})
				require.Equal(t, method, reviewReceive(t, events).Method)
			}
			hub.mu.Lock()
			require.Empty(t, hub.toolThreads)
			require.Empty(t, hub.reviewStarts)
			hub.mu.Unlock()
		})
	}
}

func TestReviewRoutingReleasesFailedAndCanceledStarts(t *testing.T) {
	for _, failure := range []string{"rejected", "malformed", "cancel", "timeout", "close"} {
		t.Run(failure, func(t *testing.T) {
			timeout := time.Second
			if failure == "timeout" {
				timeout = 100 * time.Millisecond
			}
			hub, upstream := reviewRoutingFixture(t, timeout)
			var calls atomic.Int32
			client := reviewClient(t, hub, &calls)
			events := client.Events()
			client.session.subscribe("unrelated")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			completed := make(chan error, 1)
			go func() {
				completed <- client.Call(ctx, "review/start", map[string]string{
					"threadId": "parent", "delivery": "detached",
				}, nil)
			}()
			request := reviewReceive(t, upstream.requests)
			upstream.send(t, rpcMessage{Method: "thread/started", Params: json.RawMessage(`{"thread":{"id":"new"}}`)})
			upstream.send(t, rpcMessage{Method: "item/completed", Params: json.RawMessage(`{"threadId":"unrelated"}`)})
			require.Equal(t, "item/completed", reviewReceive(t, events).Method)
			switch failure {
			case "rejected":
				upstream.send(t, rpcMessage{ID: request.ID, Error: &rpcError{Code: -32009, Message: "活动会话"}})
			case "malformed":
				upstream.send(t, rpcMessage{ID: request.ID, Result: json.RawMessage(`{"turn":{"id":"t"}}`)})
			case "cancel":
				cancel()
			case "close":
				require.NoError(t, hub.Close())
			}
			require.Error(t, reviewReceive(t, completed))
			if failure != "close" {
				require.Equal(t, "thread/started", reviewReceive(t, events).Method)
			}
			hub.mu.Lock()
			pendingCount := len(hub.reviewStarts)
			toolCount := len(hub.toolThreads)
			hub.mu.Unlock()
			require.Zero(t, pendingCount)
			require.Zero(t, toolCount)
			require.False(t, client.session.subscribed("new"), "失败响应不能建立虚假的正文订阅")
		})
	}
}

func TestReviewRoutingConcurrentStartsDoNotHoldResolvedChild(t *testing.T) {
	hub, upstream := reviewRoutingFixture(t, time.Second)
	var calls atomic.Int32
	client := reviewClient(t, hub, &calls)
	events := client.Events()
	client.session.subscribe("unrelated")
	completed := make(chan error, 2)
	for _, parent := range []string{"one", "two"} {
		go func() {
			completed <- client.Call(context.Background(), "review/start", map[string]string{
				"threadId": parent, "delivery": "detached",
			}, nil)
		}()
	}
	first := reviewReceive(t, upstream.requests)
	second := reviewReceive(t, upstream.requests)
	for _, method := range []string{"thread/started", "item/completed", "turn/completed"} {
		upstream.send(t, rpcMessage{Method: method, Params: json.RawMessage(
			`{"threadId":"child","thread":{"id":"child"},"turnId":"turn","turn":{"id":"turn"},"item":{"type":"exitedReviewMode"}}`,
		)})
	}
	upstream.send(t, rpcMessage{Method: "item/completed", Params: json.RawMessage(`{"threadId":"unrelated"}`)})
	require.JSONEq(t, `{"threadId":"unrelated"}`, string(reviewReceive(t, events).Params))
	upstream.send(t, rpcMessage{ID: first.ID, Result: json.RawMessage(`{"reviewThreadId":"child","turn":{"id":"turn"}}`)})
	require.NoError(t, reviewReceive(t, completed))
	for _, method := range []string{"thread/started", "item/completed", "turn/completed"} {
		require.Equal(t, method, reviewReceive(t, events).Method)
	}
	hub.mu.Lock()
	pendingCount, toolCount := len(hub.reviewStarts), len(hub.toolThreads)
	hub.mu.Unlock()
	require.Equal(t, 1, pendingCount, "另一个审查仍在等待 upstream 响应")
	require.Zero(t, toolCount, "提前完成的通知必须清理 owner，迟到响应不能复活回合")
	upstream.send(t, rpcMessage{ID: second.ID, Error: &rpcError{Code: -32000, Message: "审查失败"}})
	require.Error(t, reviewReceive(t, completed))
}
