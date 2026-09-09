package appserverhub

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/stretchr/testify/require"
)

func toolTestHub() *Hub {
	return &Hub{sessions: make(map[int64]*session), toolThreads: make(map[string]*toolThreadState),
		ephemeralThreads: make(map[string]bool), done: make(chan struct{})}
}

func toolTestSession(t *testing.T, hub *Hub, role Role, calls *atomic.Int32) *session {
	t.Helper()
	source, err := hub.addSession(role, nil, func(context.Context, codex.ServerRequest) (any, error) {
		calls.Add(1)
		return codex.TextToolResult(string(role), true), nil
	}, nil)
	require.NoError(t, err)
	source.desktopTools = role == RoleDesktop
	return source
}

func toolTestStart(hub *Hub, source *session, threadID, turnID string) {
	pending := hub.beginToolTurn(source, threadID)
	hub.finishToolTurnStart(threadID, pending, toolTestJSON(map[string]any{"turn": map[string]string{"id": turnID}}), nil)
}

func toolTestJSON(value any) json.RawMessage {
	data, _ := json.Marshal(value)
	return data
}

func toolTestRequest(namespace, name, turnID string) codex.ServerRequest {
	return codex.ServerRequest{ID: json.RawMessage(`1`), Method: "item/tool/call",
		Params: toolTestJSON(map[string]any{"threadId": "thread", "turnId": turnID,
			"callId": "call", "namespace": namespace, "tool": name, "arguments": map[string]any{}})}
}

func TestToolRoutingUsesOneExecutor(t *testing.T) {
	for _, tc := range []struct {
		namespace, name string
		worker          bool
	}{
		{"codex_app", "list_threads", false}, {"codex_app", "read_thread", false},
		{"codex_app", "read_thread_terminal", false}, {"future_desktop", "new_tool", false},
		{"", "desktop_tool", false}, {"", "generate_image", true},
		{"github", "issue_read", true}, {"git", "status", true},
		{"tyrs_hand", "automation_update", true}, {"browser_files", "stage_file", true},
		{"git", "unknown_tool", true},
	} {
		t.Run(tc.namespace+"/"+tc.name, func(t *testing.T) {
			hub := toolTestHub()
			var workerCalls, desktopCalls, otherCalls atomic.Int32
			toolTestSession(t, hub, RoleWorker, &workerCalls)
			desktop := toolTestSession(t, hub, RoleDesktop, &desktopCalls)
			toolTestSession(t, hub, RoleDesktop, &otherCalls)
			toolTestStart(hub, desktop, "thread", "turn")
			_, err := hub.routeToolCall(context.Background(), toolTestRequest(tc.namespace, tc.name, "turn"))
			require.NoError(t, err)
			require.Equal(t, int32(1), workerCalls.Load()+desktopCalls.Load())
			require.Equal(t, tc.worker, workerCalls.Load() == 1)
			require.Zero(t, otherCalls.Load())
		})
	}
}

func TestToolRoutingPreservesTurnOwnerAndReconnects(t *testing.T) {
	hub := toolTestHub()
	var calls atomic.Int32
	first := toolTestSession(t, hub, RoleDesktop, &calls)
	second := toolTestSession(t, hub, RoleDesktop, &calls)
	toolTestStart(hub, first, "thread", "one")
	resume := toolTestJSON(map[string]any{"thread": map[string]any{"turns": []any{
		map[string]string{"id": "one", "status": "inProgress"},
	}}})
	hub.bindDesktopTools(second, "thread", resume)
	owner, err := hub.desktopToolOwner(context.Background(), "thread", "one")
	require.NoError(t, err)
	require.Same(t, first, owner)
	hub.removeSession(first)
	owner, err = hub.desktopToolOwner(context.Background(), "thread", "one")
	require.NoError(t, err)
	require.Same(t, second, owner)
	hub.bindDesktopTools(second, "thread", resume)
	owner, err = hub.desktopToolOwner(context.Background(), "thread", "one")
	require.NoError(t, err)
	require.Same(t, second, owner)
	toolTestStart(hub, second, "thread", "two")
	hub.updateToolTurn(codex.Event{Method: "turn/completed", Params: toolTestJSON(map[string]string{"threadId": "thread", "turnId": "one"})})
	owner, err = hub.desktopToolOwner(context.Background(), "thread", "two")
	require.NoError(t, err)
	require.Same(t, second, owner)
	require.NotContains(t, hub.toolThreads["thread"].turns, "one")
	hub.updateToolTurn(codex.Event{Method: "thread/archived", Params: toolTestJSON(map[string]string{"threadId": "thread"})})
	require.Empty(t, hub.toolThreads)
}

func TestToolRoutingWorkerTurnUsesAvailableDesktop(t *testing.T) {
	hub := toolTestHub()
	var calls atomic.Int32
	worker := toolTestSession(t, hub, RoleWorker, &calls)
	desktop := toolTestSession(t, hub, RoleDesktop, &calls)
	toolTestStart(hub, worker, "thread", "turn")
	hub.bindDesktopTools(desktop, "thread", json.RawMessage(`{"thread":{"turns":[{"id":"turn","status":"inProgress"}]}}`))
	owner, err := hub.desktopToolOwner(context.Background(), "thread", "turn")
	require.NoError(t, err)
	require.Same(t, desktop, owner)
	hub.removeSession(worker)
	hub.bindDesktopTools(desktop, "thread", json.RawMessage(`{"thread":{"turns":[{"id":"turn","status":"inProgress"}]}}`))
	owner, err = hub.desktopToolOwner(context.Background(), "thread", "turn")
	require.NoError(t, err)
	require.Same(t, desktop, owner)
}

func TestToolRoutingWaitsForExactStartResponse(t *testing.T) {
	hub := toolTestHub()
	var calls atomic.Int32
	first := toolTestSession(t, hub, RoleDesktop, &calls)
	second := toolTestSession(t, hub, RoleDesktop, &calls)
	one := hub.beginToolTurn(first, "thread")
	two := hub.beginToolTurn(second, "thread")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan *session, 1)
	go func() {
		owner, _ := hub.desktopToolOwner(ctx, "thread", "two")
		result <- owner
	}()
	hub.finishToolTurnStart("thread", one, json.RawMessage(`{"turn":{"id":"one"}}`), nil)
	hub.finishToolTurnStart("thread", two, json.RawMessage(`{"turn":{"id":"two"}}`), nil)
	select {
	case owner := <-result:
		require.Same(t, second, owner)
	case <-ctx.Done():
		t.Fatal("启动响应返回后工具仍未解除等待")
	}
}

func TestToolRoutingCleansFailedAndCompletedStarts(t *testing.T) {
	for _, action := range []string{"failure", "completed", "archived", "disconnect"} {
		t.Run(action, func(t *testing.T) {
			hub := toolTestHub()
			var calls atomic.Int32
			desktop := toolTestSession(t, hub, RoleDesktop, &calls)
			pending := hub.beginToolTurn(desktop, "thread")
			var startErr error
			switch action {
			case "failure":
				startErr = errors.New("启动失败")
			case "completed", "archived":
				method := "turn/completed"
				if action == "archived" {
					method = "thread/archived"
				}
				hub.updateToolTurn(codex.Event{Method: method, Params: json.RawMessage(`{"threadId":"thread","turnId":"turn"}`)})
			case "disconnect":
				hub.removeSession(desktop)
			}
			hub.finishToolTurnStart("thread", pending, json.RawMessage(`{"turn":{"id":"turn"}}`), startErr)
			select {
			case <-pending.done:
			default:
				t.Fatal("启动等待未释放")
			}
			if action != "disconnect" {
				require.Empty(t, hub.toolThreads)
			}
		})
	}
}

func TestToolRoutingCancellationAndFailureDoNotRetry(t *testing.T) {
	hub := toolTestHub()
	var calls, otherCalls atomic.Int32
	desktop := toolTestSession(t, hub, RoleDesktop, &calls)
	toolTestSession(t, hub, RoleDesktop, &otherCalls)
	pending := hub.beginToolTurn(desktop, "thread")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := hub.desktopToolOwner(ctx, "thread", "turn")
	require.ErrorIs(t, err, context.Canceled)
	ctx, cancel = context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	_, err = hub.desktopToolOwner(ctx, "thread", "turn")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	hub.finishToolTurnStart("thread", pending, json.RawMessage(`{"turn":{"id":"turn"}}`), nil)
	desktop.handler = func(context.Context, codex.ServerRequest) (any, error) {
		calls.Add(1)
		return nil, errors.New("已执行但响应失败")
	}
	_, err = hub.routeToolCall(context.Background(), toolTestRequest("codex_app", "list_threads", "turn"))
	require.ErrorContains(t, err, "已执行但响应失败")
	require.Equal(t, int32(1), calls.Load())
	require.Zero(t, otherCalls.Load())
}

func TestToolRoutingRestoresExistingAndEphemeralThreads(t *testing.T) {
	for _, ephemeral := range []bool{false, true} {
		hub := toolTestHub()
		hub.ephemeralThreads["thread"] = ephemeral
		var calls atomic.Int32
		desktop := toolTestSession(t, hub, RoleDesktop, &calls)
		hub.bindDesktopTools(desktop, "thread", json.RawMessage(`{"thread":{"turns":[{"id":"old","status":"completed"},{"id":"turn","status":"inProgress"}]}}`))
		_, err := hub.routeToolCall(context.Background(), toolTestRequest("codex_app", "read_thread", "turn"))
		require.NoError(t, err)
		require.Equal(t, int32(1), calls.Load())
		require.NotContains(t, hub.toolThreads["thread"].turns, "old")
		_, err = hub.routeToolCall(context.Background(), toolTestRequest("git", "status", "turn"))
		require.Error(t, err)
		hub.mu.Lock()
		hub.unbindDesktopTools(desktop, "thread")
		hub.mu.Unlock()
		hub.removeSession(desktop)
		_, err = hub.desktopToolOwner(context.Background(), "thread", "turn")
		require.Error(t, err)
	}
}

func TestMobileIdentityCannotExecuteDesktopTools(t *testing.T) {
	hub := toolTestHub()
	var workerCalls, mobileCalls atomic.Int32
	toolTestSession(t, hub, RoleWorker, &workerCalls)
	mobile := toolTestSession(t, hub, RoleDesktop, &mobileCalls)
	require.NoError(t, mobile.identifyClient(json.RawMessage(`{"clientInfo":{"name":"tyrs_hand_mobile"}}`)))
	mobile.initializeDesktopTools()
	require.False(t, mobile.canExecuteDesktopTools())
	hub.bindDesktopTools(mobile, "thread", json.RawMessage(`{"thread":{"turns":[{"id":"turn","status":"inProgress"}]}}`))
	_, err := hub.desktopToolOwner(context.Background(), "thread", "turn")
	require.Error(t, err)
}
