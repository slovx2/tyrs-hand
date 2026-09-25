package codex

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/testutil/mockcodex"
	"github.com/stretchr/testify/require"
)

func TestSocketClientCancelsResolvedAndDisconnectedInteractions(t *testing.T) {
	for _, disconnected := range []bool{false, true} {
		t.Run(map[bool]string{false: "resolved", true: "disconnected"}[disconnected], func(t *testing.T) {
			server, err := mockcodex.Start(t)
			require.NoError(t, err)
			started, cancelled := make(chan struct{}), make(chan struct{})
			client, err := ConnectSocket(context.Background(), SocketClientOptions{
				SocketPath: server.SocketPath,
				ServerRequestHandler: func(ctx context.Context, _ ServerRequest) (any, error) {
					close(started)
					<-ctx.Done()
					close(cancelled)
					// 即使执行器错误地在取消后返回允许，也不能写回过期请求。
					return map[string]string{"decision": "accept"}, nil
				},
			})
			require.NoError(t, err)
			t.Cleanup(func() { _ = client.Close() })
			threadID := startSocketThread(t, client, t.TempDir())
			requestID := server.RequestServer(threadID, "item/commandExecution/requestApproval", map[string]any{"turnId": "t", "itemId": "i"})
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("没有收到审批")
			}
			if disconnected {
				require.NoError(t, client.Close())
			} else {
				server.Emit(threadID, "serverRequest/resolved", map[string]any{"threadId": "another-thread", "requestId": json.RawMessage(requestID)})
				select {
				case <-cancelled:
					t.Fatal("其他会话不能取消本审批")
				case <-time.After(30 * time.Millisecond):
				}
				server.Emit(threadID, "serverRequest/resolved", map[string]any{"threadId": threadID, "requestId": json.RawMessage(requestID)})
			}
			select {
			case <-cancelled:
			case <-time.After(time.Second):
				t.Fatal("审批回调未随生命周期取消")
			}
			require.Never(t, func() bool { _, responses, _ := server.ResolvedRequest(requestID); return responses != 0 }, 100*time.Millisecond, 5*time.Millisecond)
		})
	}
}

func TestSocketClientApprovalTimeoutRejectsLateAcceptance(t *testing.T) {
	server, err := mockcodex.Start(t)
	require.NoError(t, err)
	finished := make(chan struct{})
	client, err := ConnectSocket(context.Background(), SocketClientOptions{
		SocketPath: server.SocketPath, ServerRequestTimeout: 50 * time.Millisecond,
		ServerRequestHandler: func(ctx context.Context, _ ServerRequest) (any, error) {
			<-ctx.Done()
			close(finished)
			return map[string]string{"decision": "accept"}, nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	threadID := startSocketThread(t, client, t.TempDir())
	requestID := server.RequestServer(threadID, "item/commandExecution/requestApproval", map[string]any{"turnId": "t", "itemId": "i"})
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("审批超时没有取消执行器")
	}
	require.Eventually(t, func() bool {
		response, count, resolved := server.ResolvedRequest(requestID)
		return resolved && count == 1 && len(response) == 0
	}, time.Second, time.Millisecond)
	client.mu.Lock()
	defer client.mu.Unlock()
	require.Empty(t, client.serverRequests)
}
