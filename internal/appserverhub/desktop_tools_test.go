package appserverhub_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/appserverhub"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/testutil/mockcodex"
	"github.com/stretchr/testify/require"
)

func TestHubRoutesDesktopToolsToCallingDesktop(t *testing.T) {
	mock, err := mockcodex.Start(t)
	require.NoError(t, err)
	hub := startHub(t, mock.SocketPath)
	workerCalls := make(chan codex.ServerRequest, 4)
	worker, err := hub.OpenClient(appserverhub.ClientOptions{Role: appserverhub.RoleWorker,
		ServerRequestHandler: func(_ context.Context, request codex.ServerRequest) (any, error) {
			workerCalls <- request
			return nil, fmt.Errorf("未知 dynamic tool namespace")
		}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = worker.Close() })
	desktopCalls := make(chan codex.ServerRequest, 4)
	desktop, err := hub.OpenClient(appserverhub.ClientOptions{Role: appserverhub.RoleDesktop, DesktopTools: true,
		ServerRequestHandler: func(_ context.Context, request codex.ServerRequest) (any, error) {
			desktopCalls <- request
			return codex.TextToolResult("desktop-ok", true), nil
		}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = desktop.Close() })
	threadID, err := desktop.StartThread(context.Background(), mustJSON(map[string]any{"cwd": t.TempDir()}))
	require.NoError(t, err)
	var started struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	require.NoError(t, desktop.Call(context.Background(), "turn/start", map[string]any{"threadId": threadID}, &started))
	for _, name := range []string{"list_threads", "read_thread", "read_thread_terminal"} {
		requestID := mock.RequestDynamicTool(threadID, started.Turn.ID, name, "codex_app", name, map[string]any{})
		select {
		case request := <-desktopCalls:
			var call codex.ToolCallRequest
			require.NoError(t, json.Unmarshal(request.Params, &call))
			require.Equal(t, name, call.Tool)
		case request := <-workerCalls:
			t.Fatalf("桌面工具被错误发送给 Worker: %s", request.Params)
		case <-time.After(3 * time.Second):
			t.Fatal("桌面工具没有送达")
		}
		require.Eventually(t, func() bool {
			result, count, resolved := mock.ResolvedRequest(requestID)
			return resolved && count == 1 && string(result) != "" && json.Valid(result)
		}, time.Second, time.Millisecond)
		result, _, _ := mock.ResolvedRequest(requestID)
		require.Contains(t, string(result), "desktop-ok")
	}
}
