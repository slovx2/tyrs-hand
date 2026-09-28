package appserverhub

import (
	"encoding/json"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/stretchr/testify/require"
)

func TestMcpStreamLateEventsAndThreadUnsubscribe(t *testing.T) {
	var received []rpcMessage
	hub := &Hub{sessions: make(map[int64]*session)}
	source := newSession(1, RoleDesktop, func(message rpcMessage) error {
		received = append(received, message)
		return nil
	}, nil)
	hub.sessions[1] = source
	params := json.RawMessage(`{"subscriptionId":"same","threadId":"thread"}`)
	_, _, err := hub.scopeResourceCall(source, "mcpServer/event/stream/start", params)
	require.Error(t, err, "其他客户端订阅过的会话不能代替当前连接订阅")
	source.subscribe("thread")
	start := func() (json.RawMessage, func(error)) {
		t.Helper()
		value, finish, callErr := hub.scopeResourceCall(source, "mcpServer/event/stream/start", params)
		require.NoError(t, callErr)
		return value, finish
	}
	send := func(value json.RawMessage, method string) {
		t.Helper()
		var event map[string]any
		require.NoError(t, json.Unmarshal(value, &event))
		event["notification"] = map[string]any{"method": method, "params": map[string]any{}}
		body, marshalErr := json.Marshal(event)
		require.NoError(t, marshalErr)
		require.True(t, hub.forwardResourceEvent(codex.Event{Method: "mcpServer/event/stream/notification", Params: body}))
	}
	old, finishOld := start()
	finishOld(nil)
	_, stop, err := hub.scopeResourceCall(source, "mcpServer/event/stream/stop", params)
	require.NoError(t, err)
	stop(nil)
	current, finishCurrent := start()
	finishCurrent(nil)
	require.NotEqual(t, string(old), string(current))
	finishOld(&ProtocolError{Code: -32603, Message: "旧启动响应迟到失败"})
	require.Len(t, hub.resources, 1, "旧创建回调不能撤销新一代订阅")
	send(old, "notifications/events/event")
	require.Empty(t, received, "旧代事件必须丢弃，不能投递到复用本地 ID 的新订阅")
	send(current, "notifications/events/event")
	require.Len(t, received, 1)
	send(current, "notifications/events/terminated")
	require.Len(t, received, 2)
	require.Empty(t, hub.resources, "服务端终态必须释放本地 ID")
	next, finishNext := start()
	hub.closeThreadMcpStreams(source.id, "thread")
	finishNext(nil)
	send(next, "notifications/events/active")
	require.Len(t, received, 2, "取消 Thread 订阅后的迟到启动不能重新建立事件投递")
	require.Empty(t, hub.resources)
}
