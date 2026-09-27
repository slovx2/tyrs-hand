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

// 本测试只证明 Hub 路由的时序性质，不能替代真实 SSH/CLI 的 MCP 验收。
// 用已订阅会话的栅栏事件确认转发循环已经处理前一条通知，避免依赖 sleep 或重试。
func TestThreadStartRoutingPreservesMCPStatus(t *testing.T) {
	for _, method := range []string{"thread/start", "thread/fork", "thread/resume"} {
		for _, status := range []string{"ready", "failed", "cancelled"} {
			for _, timing := range []string{"before-response", "after-response"} {
				t.Run(method+"/"+status+"/"+timing, func(t *testing.T) {
					hub, upstream := reviewRoutingFixture(t, time.Second)
					hub.options.Controller = PassThroughController{}
					client, err := hub.OpenClient(ClientOptions{Role: RoleDesktop})
					require.NoError(t, err)
					t.Cleanup(func() { _ = client.Close() })
					client.session.subscribe("existing-thread")
					events := client.Events()
					completed := make(chan error, 1)
					requestParams := map[string]any{"cwd": "/project", "approvalPolicy": "never", "sandbox": "read-only"}
					if method != "thread/start" {
						requestParams["threadId"] = "new-thread"
					}
					go func() {
						completed <- client.Call(context.Background(), method, requestParams, nil)
					}()
					request := reviewReceive(t, upstream.requests)
					require.Equal(t, method, request.Method)
					params := map[string]any{"threadId": "new-thread", "name": "httpfixture",
						"status": status, "error": nil, "failureReason": nil}
					if status == "failed" {
						params["error"] = "fixture startup failed"
					}
					notification := rpcMessage{Method: "mcpServer/startupStatus/updated", Params: toolTestJSON(params)}
					var statuses []codex.Event
					fence := func(id string) {
						upstream.send(t, rpcMessage{Method: "item/completed", Params: toolTestJSON(map[string]any{
							"threadId": "existing-thread", "item": map[string]string{"id": id},
						})})
						for {
							event := reviewReceive(t, events)
							if event.Method == notification.Method {
								statuses = append(statuses, event)
								continue
							}
							if event.Method == "thread/started" {
								continue
							}
							var marker struct {
								ThreadID string
								Item     struct{ ID string }
							}
							require.NoError(t, json.Unmarshal(event.Params, &marker))
							require.Equal(t, "item/completed", event.Method)
							require.Equal(t, "existing-thread", marker.ThreadID)
							require.Equal(t, id, marker.Item.ID)
							return
						}
					}
					upstream.send(t, rpcMessage{Method: "thread/started", Params: json.RawMessage(`{"thread":{"id":"new-thread"}}`)})
					if timing == "before-response" {
						upstream.send(t, notification)
						// 新会话通知先到时，既有会话仍能流动，不能卡住 reader 等待响应。
						fence("before")
					}
					upstream.send(t, rpcMessage{ID: request.ID, Result: json.RawMessage(`{"thread":{"id":"new-thread"}}`)})
					require.NoError(t, reviewReceive(t, completed))
					require.True(t, client.session.subscribed("new-thread"))
					if timing == "after-response" {
						upstream.send(t, notification)
					}
					fence("after")
					require.Len(t, statuses, 1, "会话创建响应前后的 MCP 终态都必须恰好一次到达创建端")
					require.JSONEq(t, string(notification.Params), string(statuses[0].Params),
						"failed/cancelled 不能被吞成缺少 ready，也不能改写为 ready")
				})
			}
		}
	}
}

func TestThreadStartRoutingReleasesUnsuccessfulStarts(t *testing.T) {
	for _, failure := range []string{"rejected", "malformed", "cancel", "timeout", "close"} {
		t.Run(failure, func(t *testing.T) {
			timeout := time.Second
			if failure == "timeout" {
				timeout = 100 * time.Millisecond
			}
			hub, upstream := reviewRoutingFixture(t, timeout)
			hub.options.Controller = PassThroughController{}
			var calls atomic.Int32
			client := reviewClient(t, hub, &calls)
			client.session.subscribe("existing")
			events := client.Events()
			worker, err := hub.OpenClient(ClientOptions{Role: RoleWorker})
			require.NoError(t, err)
			t.Cleanup(func() { _ = worker.Close() })
			workerEvents := worker.Events()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			completed := make(chan error, 1)
			go func() { completed <- client.Call(ctx, "thread/start", map[string]any{}, nil) }()
			request := reviewReceive(t, upstream.requests)
			status := rpcMessage{Method: "mcpServer/startupStatus/updated", Params: json.RawMessage(
				`{"threadId":"new","name":"httpfixture","status":"failed","error":"denied","failureReason":null}`)}
			upstream.send(t, status)
			upstream.send(t, rpcMessage{Method: "item/completed", Params: json.RawMessage(`{"threadId":"existing"}`)})
			require.Equal(t, "item/completed", reviewReceive(t, events).Method)
			require.Equal(t, "item/completed", reviewReceive(t, workerEvents).Method)
			switch failure {
			case "rejected":
				upstream.send(t, rpcMessage{ID: request.ID, Error: &rpcError{Code: -32000, Message: "启动失败"}})
			case "malformed":
				upstream.send(t, rpcMessage{ID: request.ID, Result: json.RawMessage(`{}`)})
			case "cancel":
				cancel()
			case "close":
				require.NoError(t, hub.Close())
			}
			require.Error(t, reviewReceive(t, completed))
			require.False(t, client.session.subscribed("new"), "失败不能建立虚假订阅")
			hub.mu.Lock()
			pending := len(hub.threadStarts)
			hub.mu.Unlock()
			require.Zero(t, pending)
			if failure != "close" {
				// 失败只取消创建端的关联，不能把已有上游失败通知永久扣留。
				require.JSONEq(t, string(status.Params), string(reviewReceive(t, workerEvents).Params))
			}
		})
	}
}

func TestThreadStartRoutingConcurrentStartsResolveIndependently(t *testing.T) {
	hub, upstream := reviewRoutingFixture(t, time.Second)
	hub.options.Controller = PassThroughController{}
	var calls atomic.Int32
	client := reviewClient(t, hub, &calls)
	client.session.subscribe("existing")
	events := client.Events()
	completed := make(chan error, 2)
	for range 2 {
		go func() { completed <- client.Call(context.Background(), "thread/start", map[string]any{}, nil) }()
	}
	first, second := reviewReceive(t, upstream.requests), reviewReceive(t, upstream.requests)
	for _, thread := range []string{"one", "two"} {
		upstream.send(t, rpcMessage{Method: "mcpServer/startupStatus/updated", Params: toolTestJSON(map[string]any{
			"threadId": thread, "name": "httpfixture", "status": "ready", "error": nil, "failureReason": nil,
		})})
	}
	upstream.send(t, rpcMessage{Method: "item/completed", Params: json.RawMessage(`{"threadId":"existing"}`)})
	require.Equal(t, "item/completed", reviewReceive(t, events).Method)
	for index, request := range []rpcMessage{first, second} {
		thread := []string{"one", "two"}[index]
		upstream.send(t, rpcMessage{ID: request.ID, Result: toolTestJSON(map[string]any{"thread": map[string]string{"id": thread}})})
		require.NoError(t, reviewReceive(t, completed))
		var status struct{ ThreadID, Status string }
		event := reviewReceive(t, events)
		require.Equal(t, "mcpServer/startupStatus/updated", event.Method)
		require.NoError(t, json.Unmarshal(event.Params, &status))
		require.Equal(t, thread, status.ThreadID)
		require.Equal(t, "ready", status.Status)
		hub.mu.Lock()
		pending := len(hub.threadStarts)
		hub.mu.Unlock()
		require.Equal(t, 1-index, pending, "已关联会话不能等待另一次创建的响应")
	}
}

func TestThreadStartRoutingKeepsEarlyEphemeralEventsPrivate(t *testing.T) {
	hub, upstream := reviewRoutingFixture(t, time.Second)
	var calls atomic.Int32
	owner := reviewClient(t, hub, &calls)
	observer := reviewClient(t, hub, &calls)
	worker, err := hub.OpenClient(ClientOptions{Role: RoleWorker})
	require.NoError(t, err)
	t.Cleanup(func() { _ = worker.Close() })
	ownerEvents, observerEvents, workerEvents := owner.Events(), observer.Events(), worker.Events()
	owner.session.subscribe("existing")
	completed := make(chan error, 1)
	go func() {
		completed <- owner.Call(context.Background(), "thread/start", map[string]any{"ephemeral": true}, nil)
	}()
	request := reviewReceive(t, upstream.requests)
	upstream.send(t, rpcMessage{Method: "mcpServer/startupStatus/updated", Params: json.RawMessage(
		`{"threadId":"private","name":"httpfixture","status":"ready","error":null,"failureReason":null}`)})
	upstream.send(t, rpcMessage{Method: "item/completed", Params: json.RawMessage(`{"threadId":"existing"}`)})
	require.Equal(t, "item/completed", reviewReceive(t, ownerEvents).Method)
	require.Equal(t, "item/completed", reviewReceive(t, workerEvents).Method)
	upstream.send(t, rpcMessage{ID: request.ID, Result: json.RawMessage(`{"thread":{"id":"private","ephemeral":true}}`)})
	require.NoError(t, reviewReceive(t, completed))
	require.Equal(t, "mcpServer/startupStatus/updated", reviewReceive(t, ownerEvents).Method)
	// 无 threadId 的栅栏会广播；两端下一条必须就是栅栏，不能先收到临时会话正文。
	upstream.send(t, rpcMessage{Method: "skills/changed", Params: json.RawMessage(`{}`)})
	for _, events := range []<-chan codex.Event{ownerEvents, observerEvents, workerEvents} {
		require.Equal(t, "skills/changed", reviewReceive(t, events).Method)
	}
	require.False(t, observer.session.subscribed("private"))
	require.True(t, owner.session.subscribed("private"))
}

func TestThreadResumeRoutingBindsToolsBeforeReleasingEvents(t *testing.T) {
	hub, upstream := reviewRoutingFixture(t, time.Second)
	hub.options.Controller = PassThroughController{}
	var observerCalls, ownerCalls atomic.Int32
	_ = reviewClient(t, hub, &observerCalls)
	owner := reviewClient(t, hub, &ownerCalls)
	owner.session.subscribe("existing")
	events := owner.Events()
	completed := make(chan error, 1)
	go func() {
		completed <- owner.Call(context.Background(), "thread/resume", map[string]string{"threadId": "resumed"}, nil)
	}()
	request := reviewReceive(t, upstream.requests)
	upstream.send(t, rpcMessage{ID: json.RawMessage(`"early-tool"`), Method: "item/tool/call",
		Params: toolTestJSON(map[string]any{"threadId": "resumed", "turnId": "active",
			"callId": "early-tool", "tool": "resume_tool", "arguments": map[string]any{}})})
	upstream.send(t, rpcMessage{Method: "item/completed", Params: json.RawMessage(`{"threadId":"existing"}`)})
	require.Equal(t, "item/completed", reviewReceive(t, events).Method)
	require.Zero(t, observerCalls.Load())
	require.Zero(t, ownerCalls.Load())
	upstream.send(t, rpcMessage{ID: request.ID, Result: json.RawMessage(
		`{"thread":{"id":"resumed","turns":[{"id":"active","status":"inProgress"}]}}`)})
	require.NoError(t, reviewReceive(t, completed))
	answer := reviewReceive(t, upstream.requests)
	require.JSONEq(t, `"early-tool"`, string(answer.ID))
	require.Nil(t, answer.Error)
	require.Equal(t, int32(1), ownerCalls.Load())
	require.Zero(t, observerCalls.Load(), "更早连接的旁观端不能抢走恢复中会话的工具")
}

func TestThreadStartRoutingFailsOnHeldEventOverflow(t *testing.T) {
	hub, upstream := reviewRoutingFixture(t, time.Second)
	hub.options.Controller = PassThroughController{}
	hub.options.EventBacklog = 1
	var calls atomic.Int32
	client := reviewClient(t, hub, &calls)
	completed := make(chan error, 1)
	go func() { completed <- client.Call(context.Background(), "thread/start", map[string]any{}, nil) }()
	_ = reviewReceive(t, upstream.requests)
	for range 2 {
		upstream.send(t, rpcMessage{Method: "mcpServer/startupStatus/updated", Params: json.RawMessage(
			`{"threadId":"new","name":"httpfixture","status":"ready","error":null,"failureReason":null}`)})
	}
	_ = reviewReceive(t, hub.done)
	require.Error(t, reviewReceive(t, completed), "缓存超限必须失败，不能无界保留或静默丢弃")
}

func TestThreadStartRoutingDoesNotDelayKnownWorkerOnlyThread(t *testing.T) {
	hub, upstream := reviewRoutingFixture(t, time.Second)
	hub.options.Controller = PassThroughController{}
	worker, err := hub.OpenClient(ClientOptions{Role: RoleWorker})
	require.NoError(t, err)
	t.Cleanup(func() { _ = worker.Close() })
	events := worker.Events()
	completed := make(chan error, 1)
	go func() { completed <- worker.Call(context.Background(), "thread/start", map[string]any{}, nil) }()
	first := reviewReceive(t, upstream.requests)
	upstream.send(t, rpcMessage{ID: first.ID, Result: json.RawMessage(`{"thread":{"id":"worker-only"}}`)})
	require.NoError(t, reviewReceive(t, completed))
	// 新桌面尚未订阅原有会话，也不存在活动工具回合；身份不能依赖这两类临时状态。
	var calls atomic.Int32
	desktop := reviewClient(t, hub, &calls)
	go func() { completed <- desktop.Call(context.Background(), "thread/start", map[string]any{}, nil) }()
	second := reviewReceive(t, upstream.requests)
	upstream.send(t, rpcMessage{Method: "thread/status/changed", Params: json.RawMessage(
		`{"threadId":"worker-only","status":{"type":"idle"}}`)})
	upstream.send(t, rpcMessage{Method: "skills/changed", Params: json.RawMessage(`{}`)})
	require.Equal(t, "thread/status/changed", reviewReceive(t, events).Method,
		"已知 Worker-only 会话不能被无关创建暂扣，避免高流量撑满共享缓存")
	require.Equal(t, "skills/changed", reviewReceive(t, events).Method)
	upstream.send(t, rpcMessage{ID: second.ID, Result: json.RawMessage(`{"thread":{"id":"new"}}`)})
	require.NoError(t, reviewReceive(t, completed))
}

func TestThreadStartRoutingCanceledEphemeralDoesNotPublishUnclassifiedEvents(t *testing.T) {
	hub, upstream := reviewRoutingFixture(t, time.Second)
	worker, err := hub.OpenClient(ClientOptions{Role: RoleWorker})
	require.NoError(t, err)
	t.Cleanup(func() { _ = worker.Close() })
	workerEvents := worker.Events()
	var calls atomic.Int32
	owner := reviewClient(t, hub, &calls)
	owner.session.subscribe("existing")
	ownerEvents := owner.Events()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	completed := make(chan error, 1)
	go func() { completed <- owner.Call(ctx, "thread/start", map[string]any{"ephemeral": true}, nil) }()
	_ = reviewReceive(t, upstream.requests)
	// 启动状态先到、创建通知后到；响应仍未到达，因此不能凭缺少身份判成普通会话。
	upstream.send(t, rpcMessage{Method: "mcpServer/startupStatus/updated", Params: json.RawMessage(
		`{"threadId":"private","name":"httpfixture","status":"failed","error":"private-error","failureReason":null}`)})
	upstream.send(t, rpcMessage{Method: "thread/started", Params: json.RawMessage(`{"thread":{"id":"private","ephemeral":true}}`)})
	upstream.send(t, rpcMessage{Method: "item/completed", Params: json.RawMessage(`{"threadId":"existing"}`)})
	require.Equal(t, "item/completed", reviewReceive(t, ownerEvents).Method)
	require.Equal(t, "item/completed", reviewReceive(t, workerEvents).Method)
	cancel()
	require.Error(t, reviewReceive(t, completed))
	upstream.send(t, rpcMessage{Method: "skills/changed", Params: json.RawMessage(`{}`)})
	require.Equal(t, "skills/changed", reviewReceive(t, workerEvents).Method,
		"取消不能把待分类的私有会话失败通知释放给 Worker")
}
