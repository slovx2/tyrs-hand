package codex

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/testutil/mockcodex"
	"github.com/stretchr/testify/require"
)

func TestSocketClientFansOutEventsWithoutCrossConsumption(t *testing.T) {
	server, err := mockcodex.Start(t)
	require.NoError(t, err)
	client, err := ConnectSocket(context.Background(), SocketClientOptions{SocketPath: server.SocketPath})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	threadA := startSocketThread(t, client, "/workspace/a")
	threadB := startSocketThread(t, client, "/workspace/b")
	subscriptionA := client.Subscribe(ThreadFilter{ThreadID: threadA})
	subscriptionB := client.Subscribe(ThreadFilter{ThreadID: threadB})
	t.Cleanup(subscriptionA.Close)
	t.Cleanup(subscriptionB.Close)

	server.Emit(threadA, "item/completed", map[string]any{"threadId": threadA, "turnId": "turn-a", "item": map[string]string{"id": "a"}})
	server.Emit(threadB, "item/completed", map[string]any{"threadId": threadB, "turnId": "turn-b", "item": map[string]string{"id": "b"}})

	require.Equal(t, threadA, eventThreadID(t, receiveSocketEvent(t, subscriptionA.Events())))
	require.Equal(t, threadB, eventThreadID(t, receiveSocketEvent(t, subscriptionB.Events())))
	select {
	case event := <-subscriptionA.Events():
		t.Fatalf("Thread A 收到其他订阅事件：%s", event.Method)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSocketClientHandlesServerRequestAndPublishesResolved(t *testing.T) {
	server, err := mockcodex.Start(t)
	require.NoError(t, err)
	client, err := ConnectSocket(context.Background(), SocketClientOptions{
		SocketPath: server.SocketPath,
		ServerRequestHandler: func(_ context.Context, request ServerRequest) (any, error) {
			require.Equal(t, "item/tool/requestUserInput", request.Method)
			return map[string]any{"answers": map[string]any{"choice": map[string]any{"answers": []string{"yes"}}}}, nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	threadID := startSocketThread(t, client, "/workspace/a")
	subscription := client.Subscribe(ThreadFilter{ThreadID: threadID})
	t.Cleanup(subscription.Close)

	server.RequestUserInput(threadID, "turn-a", "item-a", []map[string]any{{
		"id": "choice", "header": "确认", "question": "继续吗？",
		"options": []map[string]string{{"label": "是", "description": "继续"}, {"label": "否", "description": "停止"}},
	}}, 60_000)

	event := receiveSocketEventMethod(t, subscription.Events(), "serverRequest/resolved")
	require.Equal(t, "serverRequest/resolved", event.Method)
}

func startSocketThread(t *testing.T, client *SocketClient, cwd string) string {
	t.Helper()
	var response struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	require.NoError(t, client.Call(context.Background(), "thread/start", map[string]any{"cwd": cwd}, &response))
	require.NotEmpty(t, response.Thread.ID)
	return response.Thread.ID
}

func receiveSocketEvent(t *testing.T, events <-chan Event) Event {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(time.Second):
		t.Fatal("等待 SocketClient 事件超时")
		return Event{}
	}
}

func receiveSocketEventMethod(t *testing.T, events <-chan Event, method string) Event {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case event := <-events:
			if event.Method == method {
				return event
			}
		case <-deadline:
			t.Fatalf("等待 SocketClient 事件 %s 超时", method)
			return Event{}
		}
	}
}

func eventThreadID(t *testing.T, event Event) string {
	t.Helper()
	var params struct {
		ThreadID string `json:"threadId"`
	}
	require.NoError(t, json.Unmarshal(event.Params, &params))
	return params.ThreadID
}
