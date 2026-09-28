//go:build integration

package hostworker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestRuntimeCodexMcpEventStreamRealSSH(t *testing.T) {
	testRuntimeRegistryRealSSH(t, "codex-mcp-stream")
}

func runtimeMcpStreamEvent(t *testing.T, ctx context.Context, events *codex.EventSubscription, id, method, marker string) {
	t.Helper()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("未收到所属连接的 MCP 事件")
		case event, ok := <-events.Events():
			require.True(t, ok)
			if event.Method != "mcpServer/event/stream/notification" {
				continue
			}
			var notification struct {
				SubscriptionID string `json:"subscriptionId"`
				Notification   struct {
					Method string
					Params struct{ Marker string }
				}
			}
			require.NoError(t, json.Unmarshal(event.Params, &notification))
			require.Equal(t, id, notification.SubscriptionID)
			require.Equal(t, method, notification.Notification.Method)
			require.Equal(t, marker, notification.Notification.Params.Marker, "另一个连接的事件不能串入")
			return
		}
	}
}

func verifyRuntimeCodexMcpEventStream(t *testing.T, ctx context.Context, connection *ssh.Client, fixture *runtimeMcpStreamFixture) {
	t.Helper()
	first, firstTrace := connectRuntimeSSHWithTrace(t, ctx, connection, runtimeidentity.Codex, codex.SocketClientOptions{})
	second, secondTrace := connectRuntimeSSHWithTrace(t, ctx, connection, runtimeidentity.Codex, codex.SocketClientOptions{})
	firstEvents, secondEvents := first.Subscribe(codex.ThreadFilter{}), second.Subscribe(codex.ThreadFilter{})
	defer firstEvents.Close()
	defer secondEvents.Close()
	// 使用官方登录入口注入仅回环测试的 JWT，不预写原生凭据或会话历史。
	claims := `{"email":"stream@example.invalid","exp":4102444800,"https://api.openai.com/auth":{"chatgpt_account_id":"stream-test-account","chatgpt_user_id":"stream-test-user","chatgpt_plan_type":"plus"}}`
	token := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".test"
	require.NoError(t, first.Call(ctx, "account/login/start", map[string]any{
		"type": "chatgptAuthTokens", "accessToken": token, "chatgptAccountId": "stream-test-account", "chatgptPlanType": "plus",
	}, nil))
	thread := readSessionThread(t, ctx, first, "thread/start", map[string]any{})
	// 新建普通会话由 Hub 订阅到两个已连接客户端；空会话尚无 rollout，不调用 resume。
	start := func(client *codex.SocketClient, id, marker string) (*runtimeMcpStream, <-chan error) {
		t.Helper()
		result := make(chan error, 1)
		go func() {
			result <- client.Call(ctx, "mcpServer/event/stream/start", map[string]any{
				"threadId": thread.ID, "server": "codex_apps", "subscriptionId": id,
				"name": "issue.updated", "arguments": map[string]any{"marker": marker}, "_meta": map[string]any{"source": marker},
			}, nil)
		}()
		select {
		case err := <-result:
			require.NoError(t, err)
			t.Fatal("服务端 active 之前不能返回订阅成功")
		case <-ctx.Done():
			t.Fatal("原生 MCP 未开始 events/stream")
		case stream := <-fixture.started:
			var params struct {
				Name      string
				Arguments struct{ Marker string }
				Meta      map[string]any `json:"_meta"`
			}
			require.NoError(t, json.Unmarshal(stream.params, &params))
			require.Equal(t, "issue.updated", params.Name)
			require.Equal(t, marker, params.Arguments.Marker)
			require.Equal(t, marker, params.Meta["source"])
			select {
			case err := <-result:
				t.Fatalf("active 前订阅已返回: %v", err)
			case <-time.After(75 * time.Millisecond):
			}
			return stream, result
		}
		return nil, nil
	}
	activate := func(stream *runtimeMcpStream, result <-chan error, events *codex.EventSubscription, id, marker string) {
		t.Helper()
		stream.event("notifications/events/active", marker)
		select {
		case err := <-result:
			require.NoError(t, err)
		case <-ctx.Done():
			t.Fatal("active 后订阅未成功")
		}
		runtimeMcpStreamEvent(t, ctx, events, id, "notifications/events/active", marker)
	}
	closed := func(stream *runtimeMcpStream) {
		t.Helper()
		select {
		case <-stream.closed:
		case <-ctx.Done():
			t.Fatal("停止或断开客户端后原生 SSE 仍未关闭")
		}
	}
	a, readyA := start(first, "same-subscription", "A")
	activate(a, readyA, firstEvents, "same-subscription", "A")
	firstTrace.expectParameterError("mcpServer/event/stream/start")
	require.Error(t, first.Call(ctx, "mcpServer/event/stream/start", map[string]any{
		"threadId": thread.ID, "server": "codex_apps", "subscriptionId": "same-subscription", "name": "issue.updated", "arguments": nil,
	}, nil), "同一客户端重复订阅仍须拒绝")
	b, readyB := start(second, "same-subscription", "B")
	activate(b, readyB, secondEvents, "same-subscription", "B")
	a.event("notifications/events/event", "A-event")
	b.event("notifications/events/event", "B-event")
	runtimeMcpStreamEvent(t, ctx, firstEvents, "same-subscription", "notifications/events/event", "A-event")
	runtimeMcpStreamEvent(t, ctx, secondEvents, "same-subscription", "notifications/events/event", "B-event")
	require.NoError(t, first.Call(ctx, "mcpServer/event/stream/stop", map[string]any{"subscriptionId": "same-subscription"}, nil))
	closed(a)
	b.event("notifications/events/event", "B-after-A-stop")
	runtimeMcpStreamEvent(t, ctx, secondEvents, "same-subscription", "notifications/events/event", "B-after-A-stop")
	// 同客户端可以重用已结束的本地 ID，但必须隔离旧代迟到事件。
	c, readyC := start(first, "same-subscription", "C")
	activate(c, readyC, firstEvents, "same-subscription", "C")
	firstTrace.expectClose("client-disconnect")
	require.NoError(t, first.Close())
	closed(c)
	b.event("notifications/events/event", "B-after-A-close")
	runtimeMcpStreamEvent(t, ctx, secondEvents, "same-subscription", "notifications/events/event", "B-after-A-close")
	b.event("notifications/events/terminated", "B-end")
	runtimeMcpStreamEvent(t, ctx, secondEvents, "same-subscription", "notifications/events/terminated", "B-end")
	closed(b)
	d, readyD := start(second, "same-subscription", "D")
	activate(d, readyD, secondEvents, "same-subscription", "D")
	require.NoError(t, second.Call(ctx, "mcpServer/event/stream/stop", map[string]any{"subscriptionId": "same-subscription"}, nil))
	closed(d)
	require.NoError(t, second.Call(ctx, "mcpServer/event/stream/stop", map[string]any{"subscriptionId": "same-subscription"}, nil))
	e, readyE := start(second, "thread-unsubscribe", "E")
	activate(e, readyE, secondEvents, "thread-unsubscribe", "E")
	require.NoError(t, second.Call(ctx, "thread/unsubscribe", map[string]any{"threadId": thread.ID}, nil))
	closed(e)
	secondTrace.expectParameterError("mcpServer/event/stream/start")
	require.Error(t, second.Call(ctx, "mcpServer/event/stream/start", map[string]any{
		"threadId": thread.ID, "server": "codex_apps", "subscriptionId": "unsubscribed", "name": "issue.updated", "arguments": nil,
	}, nil), "取消会话订阅后不能借共享上游继续订阅事件")
	require.Equal(t, int64(5), fixture.calls.Load(), "停止、断线或服务端终止不能偷偷重建订阅")
}
