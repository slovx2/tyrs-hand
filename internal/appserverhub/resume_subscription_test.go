package appserverhub_test

import (
	"context"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/appserverhub"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/testutil/mockcodex"
	"github.com/stretchr/testify/require"
)

func TestResumeOnlyResubscribesRequestingDesktop(t *testing.T) {
	mock, err := mockcodex.Start(t)
	require.NoError(t, err)
	hub := startHub(t, mock.SocketPath)
	first, err := hub.OpenClient(appserverhub.ClientOptions{Role: appserverhub.RoleDesktop})
	require.NoError(t, err)
	defer first.Close()
	second, err := hub.OpenClient(appserverhub.ClientOptions{Role: appserverhub.RoleDesktop})
	require.NoError(t, err)
	defer second.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	var response struct {
		Thread struct{ ID string }
	}
	require.NoError(t, first.Call(ctx, "thread/start", map[string]any{"cwd": t.TempDir()}, &response))
	id := response.Thread.ID
	var ignored any
	require.NoError(t, first.Call(ctx, "thread/unsubscribe", map[string]any{"threadId": id}, &ignored))
	require.NoError(t, second.Call(ctx, "thread/resume", map[string]any{"threadId": id}, &ignored))
	quiet := first.Subscribe(codex.ThreadFilter{ThreadID: id})
	defer quiet.Close()
	active := second.Subscribe(codex.ThreadFilter{ThreadID: id})
	defer active.Close()
	mock.Emit(id, "item/started", map[string]any{"threadId": id, "item": map[string]string{"id": "only-second"}})
	require.Equal(t, "item/started", receiveEvent(t, active.Events()).Method)
	select {
	case event := <-quiet.Events():
		t.Fatalf("另一端的 resume 重新订阅了已退出连接: %s", event.Method)
	case <-time.After(100 * time.Millisecond):
	}
	require.NoError(t, first.Call(ctx, "thread/resume", map[string]any{"threadId": id}, &ignored))
	mock.Emit(id, "item/started", map[string]any{"threadId": id, "item": map[string]string{"id": "explicit-resume"}})
	require.Equal(t, "item/started", receiveEvent(t, quiet.Events()).Method)
	require.Equal(t, 0, mock.RequestCount("thread/unsubscribe"), "Worker 隐式消费的原生订阅必须保留")
}
