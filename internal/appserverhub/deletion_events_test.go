package appserverhub_test

import (
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/appserverhub"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/testutil/mockcodex"
	"github.com/stretchr/testify/require"
)

// 此处仅验证 Hub 的事件路由；删除副作用另由真实 SSH/CLI 专项验证。
func TestHubBroadcastsRegularDeletionWithoutRestoringSubscription(t *testing.T) {
	mock, err := mockcodex.Start(t)
	require.NoError(t, err)
	hub := startHub(t, mock.SocketPath)
	first, err := hub.OpenClient(appserverhub.ClientOptions{Role: appserverhub.RoleDesktop})
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Close() })
	second, err := hub.OpenClient(appserverhub.ClientOptions{Role: appserverhub.RoleDesktop})
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Close() })
	var result struct{ Thread struct{ ID string } }
	require.NoError(t, first.Call(t.Context(), "thread/start", map[string]any{"cwd": t.TempDir()}, &result))
	id := result.Thread.ID
	var ignored any
	require.NoError(t, first.Call(t.Context(), "thread/unsubscribe", map[string]any{"threadId": id}, &ignored))
	firstEvents := first.Subscribe(codex.ThreadFilter{ThreadID: id})
	t.Cleanup(firstEvents.Close)
	secondEvents := second.Subscribe(codex.ThreadFilter{ThreadID: id})
	t.Cleanup(secondEvents.Close)
	mock.Emit(id, "thread/deleted", map[string]any{"threadId": id})
	for _, events := range []<-chan codex.Event{firstEvents.Events(), secondEvents.Events()} {
		for {
			select {
			case event := <-events:
				if event.Method == "thread/started" {
					continue
				}
				require.Equal(t, "thread/deleted", event.Method)
			case <-time.After(time.Second):
				t.Fatal("未订阅的列表客户端也必须收到真实上游删除通知")
			}
			break
		}
	}
	// 删除不应把退出会话的客户端重新订阅到条目正文。
	mock.Emit(id, "item/started", map[string]any{"threadId": id, "item": map[string]string{"id": "late"}})
	require.Equal(t, "item/started", receiveEvent(t, secondEvents.Events()).Method)
	select {
	case event := <-firstEvents.Events():
		t.Fatalf("删除通知不能恢复正文订阅或重复广播: %s", event.Method)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestHubKeepsEphemeralDeletionScopedToSubscribers(t *testing.T) {
	mock, err := mockcodex.Start(t)
	require.NoError(t, err)
	hub := startHub(t, mock.SocketPath)
	owner, err := hub.OpenClient(appserverhub.ClientOptions{Role: appserverhub.RoleDesktop})
	require.NoError(t, err)
	t.Cleanup(func() { _ = owner.Close() })
	outsider, err := hub.OpenClient(appserverhub.ClientOptions{Role: appserverhub.RoleDesktop})
	require.NoError(t, err)
	t.Cleanup(func() { _ = outsider.Close() })
	worker, err := hub.OpenClient(appserverhub.ClientOptions{Role: appserverhub.RoleWorker})
	require.NoError(t, err)
	t.Cleanup(func() { _ = worker.Close() })
	var result struct{ Thread struct{ ID string } }
	require.NoError(t, owner.Call(t.Context(), "thread/start", map[string]any{
		"cwd": t.TempDir(), "ephemeral": true,
	}, &result))
	id := result.Thread.ID
	owned := owner.Subscribe(codex.ThreadFilter{ThreadID: id})
	t.Cleanup(owned.Close)
	other := outsider.Subscribe(codex.ThreadFilter{ThreadID: id})
	t.Cleanup(other.Close)
	background := worker.Subscribe(codex.ThreadFilter{ThreadID: id})
	t.Cleanup(background.Close)
	mock.Emit(id, "thread/deleted", map[string]any{"threadId": id})
	for {
		event := receiveEvent(t, owned.Events())
		if event.Method == "thread/started" {
			continue
		}
		require.Equal(t, "thread/deleted", event.Method)
		break
	}
	select {
	case event := <-other.Events():
		t.Fatalf("临时会话删除泄露到其他 Desktop: %s", event.Method)
	case event := <-background.Events():
		t.Fatalf("临时会话删除泄露到 Worker: %s", event.Method)
	case <-time.After(100 * time.Millisecond):
	}
}
