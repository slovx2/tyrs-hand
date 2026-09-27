package appserverhub

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/stretchr/testify/require"
)

func canceledPrivateStart(t *testing.T, timeout time.Duration) (*Hub, *reviewUpstream, *Client, <-chan codex.Event) {
	t.Helper()
	hub, upstream := reviewRoutingFixture(t, timeout)
	worker, err := hub.OpenClient(ClientOptions{Role: RoleWorker})
	require.NoError(t, err)
	t.Cleanup(func() { _ = worker.Close() })
	events := worker.Events()
	var calls atomic.Int32
	owner := reviewClient(t, hub, &calls)
	owner.session.subscribe("existing")
	ownerEvents := owner.Events()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	completed := make(chan error, 1)
	go func() { completed <- owner.Call(ctx, "thread/start", map[string]any{"ephemeral": true}, nil) }()
	_ = reviewReceive(t, upstream.requests)
	upstream.send(t, rpcMessage{Method: "mcpServer/startupStatus/updated", Params: json.RawMessage(
		`{"threadId":"unknown","name":"httpfixture","status":"failed","error":"private-error","failureReason":null}`)})
	upstream.send(t, rpcMessage{Method: "item/completed", Params: json.RawMessage(`{"threadId":"existing"}`)})
	require.Equal(t, "item/completed", reviewReceive(t, ownerEvents).Method)
	require.Equal(t, "item/completed", reviewReceive(t, events).Method)
	cancel()
	require.Error(t, reviewReceive(t, completed))
	privacyFence(t, upstream, events)
	return hub, upstream, worker, events
}

func privacyFence(t *testing.T, upstream *reviewUpstream, events <-chan codex.Event) {
	t.Helper()
	upstream.send(t, rpcMessage{Method: "skills/changed", Params: json.RawMessage(`{}`)})
	require.Equal(t, "skills/changed", reviewReceive(t, events).Method)
}

func pendingPrivacyCount(hub *Hub) int {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	return len(hub.unclassifiedEvents)
}

func TestCanceledPrivateStartWaitsForAuthoritativeClassification(t *testing.T) {
	for _, private := range []bool{false, true} {
		name := "ordinary"
		if private {
			name = "ephemeral"
		}
		t.Run(name, func(t *testing.T) {
			hub, upstream, _, events := canceledPrivateStart(t, time.Second)
			require.Equal(t, 1, pendingPrivacyCount(hub))
			upstream.send(t, rpcMessage{Method: "thread/started", Params: toolTestJSON(map[string]any{
				"thread": map[string]any{"id": "unknown", "ephemeral": private},
			})})
			if !private {
				event := reviewReceive(t, events)
				require.Equal(t, "mcpServer/startupStatus/updated", event.Method)
				var status struct{ Status, Error string }
				require.NoError(t, json.Unmarshal(event.Params, &status))
				require.Equal(t, "failed", status.Status)
				require.Equal(t, "private-error", status.Error)
				require.Equal(t, "thread/started", reviewReceive(t, events).Method)
			}
			privacyFence(t, upstream, events)
			require.Zero(t, pendingPrivacyCount(hub))
		})
	}
}

func TestCanceledPrivateStartExpiresWithoutNewTraffic(t *testing.T) {
	hub, upstream, _, events := canceledPrivateStart(t, 100*time.Millisecond)
	require.Equal(t, 1, pendingPrivacyCount(hub))
	// 仅观察内存计数，不发送任何协议流量；清理必须由计时器触发。
	require.Eventually(t, func() bool { return pendingPrivacyCount(hub) == 0 }, time.Second, 10*time.Millisecond)
	upstream.send(t, rpcMessage{Method: "thread/started", Params: json.RawMessage(`{"thread":{"id":"unknown","ephemeral":false}}`)})
	require.Equal(t, "thread/started", reviewReceive(t, events).Method, "到期丢弃的正文不能在迟到分类后重新出现")
	privacyFence(t, upstream, events)
}

func TestCanceledPrivateStartCapacityDoesNotCloseHub(t *testing.T) {
	hub, upstream, worker, events := canceledPrivateStart(t, time.Second)
	hub.options.EventBacklog = 1
	upstream.send(t, rpcMessage{Method: "thread/status/changed", Params: json.RawMessage(`{"threadId":"another-unknown","status":{"type":"idle"}}`)})
	privacyFence(t, upstream, events)
	require.Equal(t, 1, pendingPrivacyCount(hub), "隔离容量有界")
	completed := make(chan error, 1)
	go func() { completed <- worker.Call(context.Background(), "thread/list", map[string]any{}, nil) }()
	request := reviewReceive(t, upstream.requests)
	require.Equal(t, "thread/list", request.Method)
	upstream.send(t, rpcMessage{ID: request.ID, Result: json.RawMessage(`{"data":[],"nextCursor":null}`)})
	require.NoError(t, reviewReceive(t, completed), "隔离溢出不能关闭共享 Hub")
}

func TestCanceledPrivateStartTerminalEventDiscardsBeforeReclassification(t *testing.T) {
	for _, method := range []string{"thread/closed", "thread/deleted"} {
		t.Run(method, func(t *testing.T) {
			hub, upstream, _, events := canceledPrivateStart(t, time.Second)
			upstream.send(t, rpcMessage{Method: method, Params: json.RawMessage(`{"threadId":"unknown"}`)})
			privacyFence(t, upstream, events)
			require.Zero(t, pendingPrivacyCount(hub))
			upstream.send(t, rpcMessage{Method: "thread/started", Params: json.RawMessage(`{"thread":{"id":"unknown","ephemeral":false}}`)})
			require.Equal(t, "thread/started", reviewReceive(t, events).Method,
				"已关闭代次的隔离正文不能因同 ID 再次出现而泄漏")
			privacyFence(t, upstream, events)
			upstream.send(t, rpcMessage{Method: method, Params: json.RawMessage(`{"threadId":"unknown"}`)})
			require.Equal(t, method, reviewReceive(t, events).Method)
			privacyFence(t, upstream, events)
			hub.mu.Lock()
			_, identityPresent := hub.knownThreads["unknown"]
			_, privacyPresent := hub.ephemeralThreads["unknown"]
			hub.mu.Unlock()
			require.False(t, identityPresent, "终态必须回收独立身份 registry")
			require.False(t, privacyPresent, "终态必须回收隐私分类")
		})
	}
}

func TestPrivateStartTimeoutDoesNotBlockKnownWorkerThread(t *testing.T) {
	hub, upstream := reviewRoutingFixture(t, 100*time.Millisecond)
	worker, err := hub.OpenClient(ClientOptions{Role: RoleWorker})
	require.NoError(t, err)
	t.Cleanup(func() { _ = worker.Close() })
	events := worker.Events()
	upstream.send(t, rpcMessage{Method: "thread/started", Params: json.RawMessage(`{"thread":{"id":"known","ephemeral":false}}`)})
	require.Equal(t, "thread/started", reviewReceive(t, events).Method)
	var calls atomic.Int32
	owner := reviewClient(t, hub, &calls)
	completed := make(chan error, 1)
	go func() {
		completed <- owner.Call(context.Background(), "thread/start", map[string]any{"ephemeral": true}, nil)
	}()
	_ = reviewReceive(t, upstream.requests)
	upstream.send(t, rpcMessage{Method: "thread/status/changed", Params: json.RawMessage(`{"threadId":"known","status":{"type":"idle"}}`)})
	require.Equal(t, "thread/status/changed", reviewReceive(t, events).Method)
	privacyFence(t, upstream, events)
	require.Error(t, reviewReceive(t, completed), "上游不响应时请求必须有界结束")
	upstream.send(t, rpcMessage{Method: "thread/status/changed", Params: json.RawMessage(`{"threadId":"known","status":{"type":"idle"}}`)})
	require.Equal(t, "thread/status/changed", reviewReceive(t, events).Method)
	privacyFence(t, upstream, events)
}

func TestSuccessfulPrivateThreadClosureDoesNotExposeLateEvents(t *testing.T) {
	for _, method := range []string{"thread/closed", "thread/deleted"} {
		t.Run(method, func(t *testing.T) {
			hub, upstream := reviewRoutingFixture(t, time.Second)
			worker, err := hub.OpenClient(ClientOptions{Role: RoleWorker})
			require.NoError(t, err)
			t.Cleanup(func() { _ = worker.Close() })
			events := worker.Events()
			var calls atomic.Int32
			owner := reviewClient(t, hub, &calls)
			completed := make(chan error, 1)
			go func() {
				completed <- owner.Call(context.Background(), "thread/start", map[string]any{"ephemeral": true}, nil)
			}()
			request := reviewReceive(t, upstream.requests)
			upstream.send(t, rpcMessage{ID: request.ID, Result: json.RawMessage(`{"thread":{"id":"private","ephemeral":true}}`)})
			require.NoError(t, reviewReceive(t, completed))
			hub.mu.Lock()
			guardBefore := hub.unclassifiedThreadGuard
			hub.mu.Unlock()
			require.False(t, guardBefore, "必须从成功创建而非取消留下的保护状态开始")
			upstream.send(t, rpcMessage{Method: method, Params: json.RawMessage(`{"threadId":"private"}`)})
			privacyFence(t, upstream, events)
			upstream.send(t, rpcMessage{Method: "mcpServer/startupStatus/updated", Params: json.RawMessage(
				`{"threadId":"private","name":"httpfixture","status":"failed","error":"late-private-error","failureReason":null}`)})
			privacyFence(t, upstream, events)
			require.Equal(t, 1, pendingPrivacyCount(hub))
			hub.mu.Lock()
			_, remembered := hub.knownThreads["private"]
			_, classified := hub.ephemeralThreads["private"]
			hub.mu.Unlock()
			require.False(t, remembered)
			require.False(t, classified)
		})
	}
}

func TestThreadPrivacyMetadataRequiresExplicitFlag(t *testing.T) {
	for _, malformed := range []string{`{}`, `{"thread":{"id":"unknown"}}`, `{"thread":{"ephemeral":null}}`} {
		_, classified := threadPrivacyFromMetadata(json.RawMessage(malformed))
		require.False(t, classified, "缺少 required ephemeral 字段不能被分类为普通会话")
	}
	for _, private := range []bool{false, true} {
		actual, classified := threadPrivacyFromMetadata(toolTestJSON(map[string]any{"thread": map[string]bool{"ephemeral": private}}))
		require.True(t, classified)
		require.Equal(t, private, actual)
	}
}
