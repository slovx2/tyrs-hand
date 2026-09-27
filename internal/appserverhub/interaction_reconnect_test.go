package appserverhub_test

import (
	"context"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/appserverhub"
	"github.com/slovx2/tyrs-hand/internal/testutil/mockcodex"
	"github.com/stretchr/testify/require"
)

func TestHubPendingInteractionSurvivesDesktopReconnect(t *testing.T) {
	for _, method := range []string{"item/commandExecution/requestApproval", "item/tool/requestUserInput"} {
		t.Run(method, func(t *testing.T) {
			mock, err := mockcodex.Start(t)
			require.NoError(t, err)
			hub := startHub(t, mock.SocketPath)
			first := connectDesktop(t, hub.SocketPath())
			first.initialize(t, 1)
			first.write(t, rpcMessage{ID: rawID(2), Method: "thread/start", Params: mustJSON(map[string]any{"cwd": t.TempDir()})})
			threadID := responseThreadID(t, first.response(t, rawID(2)).Result)
			requestID := mock.RequestServer(threadID, method, map[string]any{"turnId": "turn-pending", "itemId": "item-pending"})
			old := first.serverRequest(t, method)
			require.NoError(t, first.ws.Close())
			require.Never(t, func() bool {
				_, _, resolved := mock.ResolvedRequest(requestID)
				return resolved
			}, 150*time.Millisecond, 5*time.Millisecond, "客户端断线不能替用户取消待审批动作")
			second := connectDesktop(t, hub.SocketPath())
			second.initialize(t, 1)
			second.write(t, rpcMessage{ID: rawID(2), Method: "thread/resume", Params: mustJSON(map[string]any{"threadId": threadID})})
			// 恢复订阅后审批可先于 resume 响应到达，等待响应时不能丢弃它。
			require.NoError(t, second.ws.SetReadDeadline(time.Now().Add(5*time.Second)))
			var resumed rpcMessage
			responseSeen := false
			for !responseSeen || len(resumed.ID) == 0 {
				var message rpcMessage
				require.NoError(t, second.ws.ReadJSON(&message))
				if message.Method == "" && string(message.ID) == string(rawID(2)) {
					require.False(t, responseSeen)
					require.Nil(t, message.Error)
					responseSeen = true
				}
				if message.Method == method && len(message.ID) > 0 {
					require.Empty(t, resumed.ID, "重连后的审批只能投递一次")
					resumed = message
				}
			}
			require.NoError(t, second.ws.SetReadDeadline(time.Time{}))
			require.JSONEq(t, string(old.ID), string(resumed.ID))
			require.JSONEq(t, string(old.Params), string(resumed.Params))
			answer := map[string]any{"decision": "accept"}
			if method == "item/tool/requestUserInput" {
				answer = map[string]any{"answers": map[string]any{"q": map[string]any{"answers": []string{"yes"}}}}
			}
			second.write(t, rpcMessage{ID: resumed.ID, Result: mustJSON(answer)})
			require.Eventually(t, func() bool {
				_, responses, resolved := mock.ResolvedRequest(requestID)
				return resolved && responses == 1
			}, time.Second, 5*time.Millisecond)
		})
	}
}

func TestHubPendingApprovalTimeoutRejectsLateAnswer(t *testing.T) {
	mock, err := mockcodex.Start(t)
	require.NoError(t, err)
	hub, err := appserverhub.Start(context.Background(), appserverhub.Options{
		SocketPath: shortTempDir(t) + "/hub.sock", UpstreamSocketPath: mock.SocketPath,
		Controller:           appserverhub.PassThroughController{},
		ServerRequestTimeout: 100 * time.Millisecond,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, hub.Close()) })
	desktop := connectDesktop(t, hub.SocketPath())
	desktop.initialize(t, 1)
	desktop.write(t, rpcMessage{ID: rawID(2), Method: "thread/start", Params: mustJSON(map[string]any{"cwd": t.TempDir()})})
	threadID := responseThreadID(t, desktop.response(t, rawID(2)).Result)
	requestID := mock.RequestServer(threadID, "item/commandExecution/requestApproval", map[string]any{"turnId": "t", "itemId": "i"})
	request := desktop.serverRequest(t, "item/commandExecution/requestApproval")
	require.Eventually(t, func() bool {
		result, responses, resolved := mock.ResolvedRequest(requestID)
		return resolved && responses == 1 && len(result) == 0
	}, time.Second, time.Millisecond)
	desktop.write(t, rpcMessage{ID: request.ID, Result: mustJSON(map[string]any{"decision": "accept"})})
	require.Never(t, func() bool {
		result, responses, _ := mock.ResolvedRequest(requestID)
		return responses > 1 || len(result) > 0
	}, 100*time.Millisecond, 5*time.Millisecond)
}
