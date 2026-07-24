package codexrelay_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/codexrelay"
	"github.com/slovx2/tyrs-hand/internal/testutil/mockcodex"
	"github.com/stretchr/testify/require"
)

func TestRelayMultiplexesDesktopAndWorkerOverOneUpstream(t *testing.T) {
	mock, err := mockcodex.Start(t)
	require.NoError(t, err)
	relay := startRelay(t, mock.SocketPath)

	worker, err := relay.OpenClient(codexrelay.ClientOptions{Role: codexrelay.RoleWorker})
	require.NoError(t, err)
	t.Cleanup(func() { _ = worker.Close() })
	desktop := connectDesktop(t, relay.SocketPath())
	desktop.initialize(t, 1)

	desktop.write(t, rpcMessage{ID: rawID(7), Method: "thread/start",
		Params: mustJSON(map[string]any{"cwd": t.TempDir(), "approvalPolicy": "never",
			"sandbox": "read-only", "model": "mock-model"})})
	started := desktop.response(t, rawID(7))
	require.Nil(t, started.Error)
	threadID := responseThreadID(t, started.Result)

	workerEvents := worker.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	t.Cleanup(workerEvents.Close)
	desktop.write(t, rpcMessage{ID: rawID(9), Method: "turn/start", Params: mustJSON(map[string]any{
		"threadId": threadID, "input": []map[string]any{{"type": "text", "text": "hello"}},
	})})
	require.Nil(t, desktop.response(t, rawID(9)).Error)
	require.Equal(t, "turn/started", receiveEvent(t, workerEvents.Events()).Method)

	require.Equal(t, int64(1), relay.Stats().UpstreamConnections)
	require.Equal(t, int64(1), relay.Stats().UpstreamInitializations)
}

func TestRelayBroadcastsNewThreadsCreatedByWorkerToDesktop(t *testing.T) {
	mock, err := mockcodex.Start(t)
	require.NoError(t, err)
	relay := startRelay(t, mock.SocketPath)
	worker, err := relay.OpenClient(codexrelay.ClientOptions{Role: codexrelay.RoleWorker})
	require.NoError(t, err)
	t.Cleanup(func() { _ = worker.Close() })
	desktop := connectDesktop(t, relay.SocketPath())
	desktop.initialize(t, 1)

	threadID, err := worker.StartThread(context.Background(), mustJSON(map[string]any{"cwd": t.TempDir()}))
	require.NoError(t, err)
	started := desktop.notification(t, "thread/started")
	require.Equal(t, threadID, eventThreadID(t, started.Params))
}

func TestRelayKeepsSubscriptionUntilLastClientLeaves(t *testing.T) {
	mock, err := mockcodex.Start(t)
	require.NoError(t, err)
	relay := startRelay(t, mock.SocketPath)
	worker, err := relay.OpenClient(codexrelay.ClientOptions{Role: codexrelay.RoleWorker})
	require.NoError(t, err)
	t.Cleanup(func() { _ = worker.Close() })
	desktop := connectDesktop(t, relay.SocketPath())
	desktop.initialize(t, 1)

	threadID, err := worker.StartThread(context.Background(), mustJSON(map[string]any{
		"cwd": t.TempDir(), "approvalPolicy": "never", "sandbox": "read-only",
	}))
	require.NoError(t, err)
	desktop.write(t, rpcMessage{ID: rawID(2), Method: "thread/resume",
		Params: mustJSON(map[string]string{"threadId": threadID})})
	require.Nil(t, desktop.response(t, rawID(2)).Error)
	desktop.write(t, rpcMessage{ID: rawID(3), Method: "thread/unsubscribe",
		Params: mustJSON(map[string]string{"threadId": threadID})})
	require.Nil(t, desktop.response(t, rawID(3)).Error)

	subscription := worker.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	t.Cleanup(subscription.Close)
	mock.Emit(threadID, "item/started", map[string]any{"threadId": threadID,
		"item": map[string]any{"id": "still-live", "type": "commandExecution"}})
	require.Equal(t, "item/started", receiveEvent(t, subscription.Events()).Method)
}

func TestRelayRoutesDynamicToolsOnlyToWorker(t *testing.T) {
	mock, err := mockcodex.Start(t)
	require.NoError(t, err)
	relay := startRelay(t, mock.SocketPath)
	toolCalls := make(chan codex.ServerRequest, 1)
	worker, err := relay.OpenClient(codexrelay.ClientOptions{Role: codexrelay.RoleWorker,
		ServerRequestHandler: func(_ context.Context, request codex.ServerRequest) (any, error) {
			toolCalls <- request
			return codex.TextToolResult("worker-ok", true), nil
		}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = worker.Close() })
	desktop := connectDesktop(t, relay.SocketPath())
	desktop.initialize(t, 1)
	threadID, err := worker.StartThread(context.Background(), mustJSON(map[string]any{
		"cwd": t.TempDir(), "approvalPolicy": "never", "sandbox": "read-only",
	}))
	require.NoError(t, err)
	desktop.write(t, rpcMessage{ID: rawID(2), Method: "thread/resume",
		Params: mustJSON(map[string]string{"threadId": threadID})})
	require.Nil(t, desktop.response(t, rawID(2)).Error)

	requestID := mock.RequestDynamicTool(threadID, "turn-1", "call-1", "github", "echo",
		map[string]any{"message": "hello"})
	select {
	case request := <-toolCalls:
		require.Equal(t, "item/tool/call", request.Method)
	case <-time.After(3 * time.Second):
		t.Fatal("Worker 没有收到动态工具请求")
	}
	result, responses, resolved := mock.ResolvedRequest(requestID)
	require.Eventually(t, func() bool {
		result, responses, resolved = mock.ResolvedRequest(requestID)
		return resolved
	}, 3*time.Second, 10*time.Millisecond)
	require.Equal(t, 1, responses)
	require.Contains(t, string(result), "worker-ok")
	desktop.expectNoServerRequest(t, 150*time.Millisecond)
}

func TestRelayAllowsCollidingRequestIDsAcrossDesktopClients(t *testing.T) {
	mock, err := mockcodex.Start(t)
	require.NoError(t, err)
	relay := startRelay(t, mock.SocketPath)
	first := connectDesktop(t, relay.SocketPath())
	second := connectDesktop(t, relay.SocketPath())
	first.initialize(t, 1)
	second.initialize(t, 1)

	first.write(t, rpcMessage{ID: rawID(42), Method: "thread/start", Params: mustJSON(map[string]any{
		"cwd": t.TempDir(), "approvalPolicy": "never", "sandbox": "read-only",
	})})
	second.write(t, rpcMessage{ID: rawID(42), Method: "thread/start", Params: mustJSON(map[string]any{
		"cwd": t.TempDir(), "approvalPolicy": "never", "sandbox": "read-only",
	})})
	require.NotEqual(t, responseThreadID(t, first.response(t, rawID(42)).Result),
		responseThreadID(t, second.response(t, rawID(42)).Result))
	require.Equal(t, int64(1), relay.Stats().UpstreamConnections)
}

func TestRelayPreservesDesktopConfigurationAndFutureMethodAccess(t *testing.T) {
	mock, err := mockcodex.Start(t)
	require.NoError(t, err)
	relay := startRelay(t, mock.SocketPath)
	desktop := connectDesktop(t, relay.SocketPath())
	desktop.initialize(t, 1)

	desktop.write(t, rpcMessage{ID: rawID(2), Method: "future/mutate", Params: mustJSON(map[string]any{})})
	unknown := desktop.response(t, rawID(2))
	require.Equal(t, -32601, rpcErrorCode(t, unknown.Error))
	desktop.write(t, rpcMessage{ID: rawID(3), Method: "config/value/write",
		Params: mustJSON(map[string]any{"keyPath": "approval_policy", "value": "on-request"})})
	forwarded := desktop.response(t, rawID(3))
	require.Equal(t, -32601, rpcErrorCode(t, forwarded.Error),
		"错误应来自 mock app-server，而不是 Relay 的安全拦截")

	received := map[string]bool{}
	deadline := time.After(time.Second)
	for len(received) < 2 {
		select {
		case request := <-mock.Requests():
			if request.Message.Method == "future/mutate" || request.Message.Method == "config/value/write" {
				received[request.Message.Method] = true
			}
		case <-deadline:
			t.Fatalf("Desktop 方法没有透明到达上游: %#v", received)
		}
	}
}

func TestRelayAllowsDesktopAccountCapabilityProjectionWithoutChangingWorker(t *testing.T) {
	mock, err := mockcodex.Start(t)
	require.NoError(t, err)
	directory := shortTempDir(t)
	relay, err := codexrelay.Start(context.Background(), codexrelay.Options{
		SocketPath: directory + "/relay.sock", UpstreamSocketPath: mock.SocketPath,
		Controller: desktopAccountController{},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, relay.Close()) })

	worker, err := relay.OpenClient(codexrelay.ClientOptions{Role: codexrelay.RoleWorker})
	require.NoError(t, err)
	t.Cleanup(func() { _ = worker.Close() })
	var workerAccount map[string]any
	require.NoError(t, worker.Call(context.Background(), "account/read",
		map[string]any{"refreshToken": false}, &workerAccount))
	require.Equal(t, "apiKey", workerAccount["account"].(map[string]any)["type"])

	desktop := connectDesktop(t, relay.SocketPath())
	desktop.initialize(t, 1)
	desktop.write(t, rpcMessage{ID: rawID(2), Method: "account/read",
		Params: mustJSON(map[string]any{"refreshToken": false})})
	response := desktop.response(t, rawID(2))
	require.Nil(t, response.Error)
	require.JSONEq(t, `{"account":{"type":"chatgpt","email":null,"planType":"unknown"},`+
		`"requiresOpenaiAuth":false}`, string(response.Result))
}

func TestRelayRequiresControllerForDesktopControlCalls(t *testing.T) {
	mock, err := mockcodex.Start(t)
	require.NoError(t, err)
	directory := shortTempDir(t)
	relay, err := codexrelay.Start(context.Background(), codexrelay.Options{
		SocketPath: directory + "/relay.sock", UpstreamSocketPath: mock.SocketPath,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, relay.Close()) })
	desktop := connectDesktop(t, relay.SocketPath())
	desktop.initialize(t, 1)
	desktop.write(t, rpcMessage{ID: rawID(2), Method: "thread/start",
		Params: mustJSON(map[string]any{"cwd": t.TempDir()})})
	require.Equal(t, -32041, rpcErrorCode(t, desktop.response(t, rawID(2)).Error))
}

type desktopAccountController struct{}

func (desktopAccountController) PrepareCall(_ context.Context,
	call codexrelay.Call,
) (codexrelay.CallPlan, error) {
	return codexrelay.CallPlan{Params: call.Params, Forward: true}, nil
}

func (desktopAccountController) CompleteCall(_ context.Context, call codexrelay.Call,
	_ codexrelay.CallPlan, result json.RawMessage, cause error,
) (json.RawMessage, error) {
	if call.Method == "account/read" {
		return json.RawMessage(`{"account":{"type":"chatgpt","email":null,` +
			`"planType":"unknown"},"requiresOpenaiAuth":false}`), nil
	}
	return result, cause
}

func (desktopAccountController) ResolveInteractive(_ context.Context, _ codex.ServerRequest,
	answer json.RawMessage, _ codexrelay.Role,
) (bool, json.RawMessage, error) {
	return true, answer, nil
}

func TestRelayPreservesUpstreamJSONRPCError(t *testing.T) {
	mock, err := mockcodex.Start(t)
	require.NoError(t, err)
	relay := startRelay(t, mock.SocketPath)
	desktop := connectDesktop(t, relay.SocketPath())
	desktop.initialize(t, 1)
	desktop.write(t, rpcMessage{ID: rawID(2), Method: "thread/resume",
		Params: mustJSON(map[string]string{"threadId": "missing"})})
	require.Equal(t, -32602, rpcErrorCode(t, desktop.response(t, rawID(2)).Error))
}

func TestRelayWorkerSubscriptionDoesNotDependOnGlobalEventQueue(t *testing.T) {
	mock, err := mockcodex.Start(t)
	require.NoError(t, err)
	directory := shortTempDir(t)
	relay, err := codexrelay.Start(context.Background(), codexrelay.Options{
		SocketPath: directory + "/relay.sock", UpstreamSocketPath: mock.SocketPath,
		EventBacklog: 1,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, relay.Close()) })
	worker, err := relay.OpenClient(codexrelay.ClientOptions{Role: codexrelay.RoleWorker,
		EventBacklog: 1})
	require.NoError(t, err)
	t.Cleanup(func() { _ = worker.Close() })
	startedEvents := worker.Subscribe(codex.ThreadFilter{})
	threadID, err := worker.StartThread(context.Background(), mustJSON(map[string]any{"cwd": t.TempDir()}))
	require.NoError(t, err)
	require.Equal(t, "thread/started", receiveEvent(t, startedEvents.Events()).Method)
	startedEvents.Close()
	subscription := worker.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	t.Cleanup(subscription.Close)

	for index := 0; index < 3; index++ {
		mock.Emit(threadID, "item/started", map[string]any{"threadId": threadID,
			"item": map[string]any{"id": strconv.Itoa(index), "type": "commandExecution"}})
		require.Equal(t, "item/started", receiveEvent(t, subscription.Events()).Method)
	}
}

func TestRelayRequestUserInputDesktopWinsAndUpstreamReceivesOneAnswer(t *testing.T) {
	mock, err := mockcodex.Start(t)
	require.NoError(t, err)
	relay := startRelay(t, mock.SocketPath)
	workerStarted := make(chan struct{}, 1)
	worker, err := relay.OpenClient(codexrelay.ClientOptions{Role: codexrelay.RoleWorker,
		ServerRequestHandler: func(ctx context.Context, request codex.ServerRequest) (any, error) {
			if request.Method != "item/tool/requestUserInput" {
				return nil, nil
			}
			workerStarted <- struct{}{}
			<-ctx.Done()
			return nil, ctx.Err()
		}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = worker.Close() })
	desktop := connectDesktop(t, relay.SocketPath())
	desktop.initialize(t, 1)
	threadID, err := worker.StartThread(context.Background(), mustJSON(map[string]any{
		"cwd": t.TempDir(), "approvalPolicy": "never", "sandbox": "read-only",
	}))
	require.NoError(t, err)
	desktop.write(t, rpcMessage{ID: rawID(2), Method: "thread/resume",
		Params: mustJSON(map[string]string{"threadId": threadID})})
	require.Nil(t, desktop.response(t, rawID(2)).Error)

	requestID := mock.RequestUserInput(threadID, "turn-input", "item-input", []map[string]any{{
		"id": "choice", "header": "Choose", "question": "Continue?",
	}}, 60_000)
	request := desktop.serverRequest(t, "item/tool/requestUserInput")
	require.Equal(t, requestID, string(request.ID))
	require.NotContains(t, string(request.Params), "autoResolutionMs")
	select {
	case <-workerStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("Worker 没有同时收到 requestUserInput")
	}
	desktop.write(t, rpcMessage{ID: request.ID,
		Result: mustJSON(map[string]any{"answers": map[string]any{"choice": map[string]any{
			"answers": []string{"yes"}}}})})
	require.Eventually(t, func() bool {
		_, responses, resolved := mock.ResolvedRequest(requestID)
		return resolved && responses == 1
	}, 3*time.Second, 10*time.Millisecond)
}

func TestRelayWorkerWinsInputAfterDesktopDisconnects(t *testing.T) {
	mock, err := mockcodex.Start(t)
	require.NoError(t, err)
	relay := startRelay(t, mock.SocketPath)
	workerReceived := make(chan struct{}, 1)
	worker, err := relay.OpenClient(codexrelay.ClientOptions{Role: codexrelay.RoleWorker,
		ServerRequestHandler: func(_ context.Context, request codex.ServerRequest) (any, error) {
			workerReceived <- struct{}{}
			return map[string]any{"answers": map[string]any{"choice": map[string]any{
				"answers": []string{"worker"}}}}, nil
		}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = worker.Close() })
	desktop := connectDesktop(t, relay.SocketPath())
	desktop.initialize(t, 1)
	threadID, err := worker.StartThread(context.Background(), mustJSON(map[string]any{"cwd": t.TempDir()}))
	require.NoError(t, err)
	desktop.write(t, rpcMessage{ID: rawID(2), Method: "thread/resume",
		Params: mustJSON(map[string]string{"threadId": threadID})})
	require.Nil(t, desktop.response(t, rawID(2)).Error)
	require.NoError(t, desktop.ws.Close())

	requestID := mock.RequestUserInput(threadID, "turn-1", "item-1", []map[string]any{{
		"id": "choice", "header": "Choose", "question": "Continue?",
	}}, 60_000)
	select {
	case <-workerReceived:
	case <-time.After(3 * time.Second):
		t.Fatal("Desktop 断线后 Worker 没有收到 requestUserInput")
	}
	require.Eventually(t, func() bool {
		_, responses, resolved := mock.ResolvedRequest(requestID)
		return resolved && responses == 1
	}, 3*time.Second, 10*time.Millisecond)
}

func TestRelayPreservesOrdinaryDesktopServerRequests(t *testing.T) {
	mock, err := mockcodex.Start(t)
	require.NoError(t, err)
	relay := startRelay(t, mock.SocketPath)
	workerCalls := make(chan codex.ServerRequest, 1)
	worker, err := relay.OpenClient(codexrelay.ClientOptions{Role: codexrelay.RoleWorker,
		ServerRequestHandler: func(_ context.Context, request codex.ServerRequest) (any, error) {
			workerCalls <- request
			return nil, nil
		}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = worker.Close() })
	desktop := connectDesktop(t, relay.SocketPath())
	desktop.initialize(t, 1)
	threadID, err := worker.StartThread(context.Background(), mustJSON(map[string]any{"cwd": t.TempDir()}))
	require.NoError(t, err)
	desktop.write(t, rpcMessage{ID: rawID(2), Method: "thread/resume",
		Params: mustJSON(map[string]string{"threadId": threadID})})
	require.Nil(t, desktop.response(t, rawID(2)).Error)

	requestID := mock.RequestServer(threadID, "item/commandExecution/requestApproval",
		map[string]any{"turnId": "turn-approval", "itemId": "command-approval"})
	request := desktop.serverRequest(t, "item/commandExecution/requestApproval")
	desktop.write(t, rpcMessage{ID: request.ID, Result: mustJSON(map[string]string{
		"decision": "accept",
	})})
	require.Eventually(t, func() bool {
		_, responses, resolved := mock.ResolvedRequest(requestID)
		return resolved && responses == 1
	}, 3*time.Second, 10*time.Millisecond)
	select {
	case request := <-workerCalls:
		t.Fatalf("普通 Desktop Server Request 不应发送给 Worker: %s", request.Method)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestRelaySynchronizesSteerInterruptAndRejectsConcurrentStart(t *testing.T) {
	mock, err := mockcodex.Start(t)
	require.NoError(t, err)
	relay := startRelay(t, mock.SocketPath)
	worker, err := relay.OpenClient(codexrelay.ClientOptions{Role: codexrelay.RoleWorker})
	require.NoError(t, err)
	t.Cleanup(func() { _ = worker.Close() })
	first := connectDesktop(t, relay.SocketPath())
	second := connectDesktop(t, relay.SocketPath())
	first.initialize(t, 1)
	second.initialize(t, 1)

	first.write(t, rpcMessage{ID: rawID(2), Method: "thread/start",
		Params: mustJSON(map[string]any{"cwd": t.TempDir()})})
	threadID := responseThreadID(t, first.response(t, rawID(2)).Result)
	second.write(t, rpcMessage{ID: rawID(2), Method: "thread/resume",
		Params: mustJSON(map[string]string{"threadId": threadID})})
	require.Nil(t, second.response(t, rawID(2)).Error)
	events := worker.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	t.Cleanup(events.Close)

	first.write(t, rpcMessage{ID: rawID(3), Method: "turn/start",
		Params: mustJSON(map[string]any{"threadId": threadID,
			"input": []map[string]string{{"type": "text", "text": "first"}}})})
	started := first.response(t, rawID(3))
	require.Nil(t, started.Error)
	_, turnID := testResponseScope(t, started.Result)
	require.Equal(t, "turn/started", receiveEvent(t, events.Events()).Method)

	second.write(t, rpcMessage{ID: rawID(3), Method: "turn/start",
		Params: mustJSON(map[string]any{"threadId": threadID,
			"input": []map[string]string{{"type": "text", "text": "conflict"}}})})
	require.Equal(t, -32000, rpcErrorCode(t, second.response(t, rawID(3)).Error))
	require.NoError(t, worker.Call(context.Background(), "turn/steer", map[string]any{
		"threadId": threadID, "expectedTurnId": turnID,
		"input": []map[string]string{{"type": "text", "text": "worker steer"}},
	}, nil))
	require.Equal(t, "item/started", second.notification(t, "item/started").Method)
	require.Equal(t, "item/started", receiveEvent(t, events.Events()).Method)

	first.write(t, rpcMessage{ID: rawID(4), Method: "turn/interrupt",
		Params: mustJSON(map[string]string{"threadId": threadID, "turnId": turnID})})
	require.Nil(t, first.response(t, rawID(4)).Error)
	require.Equal(t, "turn/completed", receiveEvent(t, events.Events()).Method)
}

func TestRelayWaitsForActiveTurnBeforeArchiving(t *testing.T) {
	mock, err := mockcodex.Start(t)
	require.NoError(t, err)
	relay := startRelay(t, mock.SocketPath)
	worker, err := relay.OpenClient(codexrelay.ClientOptions{Role: codexrelay.RoleWorker})
	require.NoError(t, err)
	t.Cleanup(func() { _ = worker.Close() })
	first := connectDesktop(t, relay.SocketPath())
	second := connectDesktop(t, relay.SocketPath())
	first.initialize(t, 1)
	second.initialize(t, 1)

	first.write(t, rpcMessage{ID: rawID(2), Method: "thread/start",
		Params: mustJSON(map[string]any{"cwd": t.TempDir()})})
	threadID := responseThreadID(t, first.response(t, rawID(2)).Result)
	second.write(t, rpcMessage{ID: rawID(2), Method: "thread/resume",
		Params: mustJSON(map[string]string{"threadId": threadID})})
	require.Nil(t, second.response(t, rawID(2)).Error)
	events := worker.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	t.Cleanup(events.Close)

	first.write(t, rpcMessage{ID: rawID(3), Method: "turn/start",
		Params: mustJSON(map[string]any{"threadId": threadID,
			"input": []map[string]string{{"type": "text", "text": "keep running"}}})})
	turnStarted := first.response(t, rawID(3))
	require.Nil(t, turnStarted.Error)
	_, turnID := testResponseScope(t, turnStarted.Result)
	require.Equal(t, "turn/started", receiveEvent(t, events.Events()).Method)

	first.write(t, rpcMessage{ID: rawID(4), Method: "thread/archive",
		Params: mustJSON(map[string]string{"threadId": threadID})})
	require.Never(t, func() bool {
		return mock.RequestCount("thread/archive") != 0
	}, 250*time.Millisecond, 10*time.Millisecond,
		"活动 Turn 完成前不应调用官方 archive")

	second.write(t, rpcMessage{ID: rawID(3), Method: "turn/steer",
		Params: mustJSON(map[string]any{"threadId": threadID, "expectedTurnId": turnID,
			"input": []map[string]string{{"type": "text", "text": "late steer"}}})})
	require.Equal(t, -32052, rpcErrorCode(t, second.response(t, rawID(3)).Error))

	require.True(t, mock.CompleteTurn(threadID, turnID, "done"))
	require.Equal(t, "item/completed", receiveEvent(t, events.Events()).Method)
	require.Equal(t, "turn/completed", receiveEvent(t, events.Events()).Method)
	archiveResponse := first.response(t, rawID(4))
	require.Nil(t, archiveResponse.Error)
	require.Equal(t, 1, mock.RequestCount("thread/archive"))
	require.Equal(t, "thread/archived", second.notification(t, "thread/archived").Method)
}

func TestRelayArchivesImmediatelyWhenIdleAndUnarchivesForEveryClient(t *testing.T) {
	mock, err := mockcodex.Start(t)
	require.NoError(t, err)
	relay := startRelay(t, mock.SocketPath)
	worker, err := relay.OpenClient(codexrelay.ClientOptions{Role: codexrelay.RoleWorker})
	require.NoError(t, err)
	t.Cleanup(func() { _ = worker.Close() })
	first := connectDesktop(t, relay.SocketPath())
	second := connectDesktop(t, relay.SocketPath())
	first.initialize(t, 1)
	second.initialize(t, 1)

	first.write(t, rpcMessage{ID: rawID(2), Method: "thread/start",
		Params: mustJSON(map[string]any{"cwd": t.TempDir()})})
	threadID := responseThreadID(t, first.response(t, rawID(2)).Result)
	second.write(t, rpcMessage{ID: rawID(2), Method: "thread/resume",
		Params: mustJSON(map[string]string{"threadId": threadID})})
	require.Nil(t, second.response(t, rawID(2)).Error)
	events := worker.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	t.Cleanup(events.Close)

	first.write(t, rpcMessage{ID: rawID(3), Method: "thread/archive",
		Params: mustJSON(map[string]string{"threadId": threadID})})
	require.Nil(t, first.response(t, rawID(3)).Error)
	require.Equal(t, "thread/archived", second.notification(t, "thread/archived").Method)
	require.Equal(t, "thread/archived", receiveEvent(t, events.Events()).Method)

	second.write(t, rpcMessage{ID: rawID(3), Method: "thread/unarchive",
		Params: mustJSON(map[string]string{"threadId": threadID})})
	require.Nil(t, second.response(t, rawID(3)).Error)
	require.Equal(t, "thread/unarchived", first.notification(t, "thread/unarchived").Method)
	require.Equal(t, "thread/unarchived", receiveEvent(t, events.Events()).Method)
	require.Equal(t, 1, mock.RequestCount("thread/archive"))
	require.Equal(t, 1, mock.RequestCount("thread/unarchive"))
}

func TestRelayCoalescesConcurrentArchiveRequests(t *testing.T) {
	mock, err := mockcodex.Start(t)
	require.NoError(t, err)
	relay := startRelay(t, mock.SocketPath)
	worker, err := relay.OpenClient(codexrelay.ClientOptions{Role: codexrelay.RoleWorker})
	require.NoError(t, err)
	t.Cleanup(func() { _ = worker.Close() })
	first := connectDesktop(t, relay.SocketPath())
	second := connectDesktop(t, relay.SocketPath())
	first.initialize(t, 1)
	second.initialize(t, 1)

	first.write(t, rpcMessage{ID: rawID(2), Method: "thread/start",
		Params: mustJSON(map[string]any{"cwd": t.TempDir()})})
	threadID := responseThreadID(t, first.response(t, rawID(2)).Result)
	second.write(t, rpcMessage{ID: rawID(2), Method: "thread/resume",
		Params: mustJSON(map[string]string{"threadId": threadID})})
	require.Nil(t, second.response(t, rawID(2)).Error)
	var turn struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	require.NoError(t, worker.Call(context.Background(), "turn/start", map[string]any{
		"threadId": threadID,
		"input":    []map[string]string{{"type": "text", "text": "running"}},
	}, &turn))
	first.write(t, rpcMessage{ID: rawID(3), Method: "thread/archive",
		Params: mustJSON(map[string]string{"threadId": threadID})})
	second.write(t, rpcMessage{ID: rawID(3), Method: "thread/archive",
		Params: mustJSON(map[string]string{"threadId": threadID})})
	require.True(t, mock.CompleteTurn(threadID, turn.Turn.ID, "done"))
	require.Nil(t, first.response(t, rawID(3)).Error)
	require.Nil(t, second.response(t, rawID(3)).Error)
	require.Equal(t, 1, mock.RequestCount("thread/archive"))
}

func TestRelayArchivePendingAllowsInputAnswerAndInterrupt(t *testing.T) {
	mock, err := mockcodex.Start(t)
	require.NoError(t, err)
	relay := startRelay(t, mock.SocketPath)
	worker, err := relay.OpenClient(codexrelay.ClientOptions{Role: codexrelay.RoleWorker})
	require.NoError(t, err)
	t.Cleanup(func() { _ = worker.Close() })
	desktop := connectDesktop(t, relay.SocketPath())
	desktop.initialize(t, 1)
	threadID, err := worker.StartThread(context.Background(), mustJSON(map[string]any{
		"cwd": t.TempDir(),
	}))
	require.NoError(t, err)
	desktop.write(t, rpcMessage{ID: rawID(2), Method: "thread/resume",
		Params: mustJSON(map[string]string{"threadId": threadID})})
	require.Nil(t, desktop.response(t, rawID(2)).Error)
	var turn struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	require.NoError(t, worker.Call(context.Background(), "turn/start", map[string]any{
		"threadId": threadID,
		"input":    []map[string]string{{"type": "text", "text": "running"}},
	}, &turn))
	desktop.write(t, rpcMessage{ID: rawID(3), Method: "thread/archive",
		Params: mustJSON(map[string]string{"threadId": threadID})})

	requestID := mock.RequestUserInput(threadID, turn.Turn.ID, "input-1",
		[]map[string]any{{"id": "answer", "header": "继续", "question": "继续吗？"}}, 1000)
	request := desktop.serverRequest(t, "item/tool/requestUserInput")
	desktop.write(t, rpcMessage{ID: request.ID, Result: mustJSON(map[string]any{
		"answers": map[string]any{"answer": map[string]any{"answers": []string{"继续"}}},
	})})
	require.Eventually(t, func() bool {
		_, responses, resolved := mock.ResolvedRequest(requestID)
		return resolved && responses == 1
	}, time.Second, 10*time.Millisecond)
	desktop.write(t, rpcMessage{ID: rawID(4), Method: "turn/interrupt",
		Params: mustJSON(map[string]string{"threadId": threadID, "turnId": turn.Turn.ID})})
	responses := desktop.responses(t, rawID(3), rawID(4))
	require.Nil(t, responses[string(rawID(4))].Error)
	require.Nil(t, responses[string(rawID(3))].Error)
	require.Equal(t, 1, mock.RequestCount("thread/archive"))
}

func TestRelayCancelsWaitingArchiveOnUnarchive(t *testing.T) {
	mock, err := mockcodex.Start(t)
	require.NoError(t, err)
	relay := startRelay(t, mock.SocketPath)
	worker, err := relay.OpenClient(codexrelay.ClientOptions{Role: codexrelay.RoleWorker})
	require.NoError(t, err)
	t.Cleanup(func() { _ = worker.Close() })
	desktop := connectDesktop(t, relay.SocketPath())
	desktop.initialize(t, 1)
	threadID, err := worker.StartThread(context.Background(), mustJSON(map[string]any{
		"cwd": t.TempDir(),
	}))
	require.NoError(t, err)
	desktop.write(t, rpcMessage{ID: rawID(2), Method: "thread/resume",
		Params: mustJSON(map[string]string{"threadId": threadID})})
	require.Nil(t, desktop.response(t, rawID(2)).Error)
	var turn struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	require.NoError(t, worker.Call(context.Background(), "turn/start", map[string]any{
		"threadId": threadID,
		"input":    []map[string]string{{"type": "text", "text": "running"}},
	}, &turn))
	desktop.write(t, rpcMessage{ID: rawID(3), Method: "thread/archive",
		Params: mustJSON(map[string]string{"threadId": threadID})})
	require.Eventually(t, func() bool {
		return mock.RequestCount("thread/read") > 0
	}, time.Second, 10*time.Millisecond)
	require.NoError(t, worker.Call(context.Background(), "thread/unarchive",
		map[string]string{"threadId": threadID}, nil))
	require.Equal(t, -32053, rpcErrorCode(t, desktop.response(t, rawID(3)).Error))
	require.True(t, mock.CompleteTurn(threadID, turn.Turn.ID, "done"))
	require.Never(t, func() bool {
		return mock.RequestCount("thread/archive") != 0
	}, 200*time.Millisecond, 10*time.Millisecond)
}

func TestRelayArchiveDisconnectAndTimeoutDoNotReachUpstream(t *testing.T) {
	for _, test := range []struct {
		name       string
		disconnect bool
	}{
		{name: "disconnect", disconnect: true},
		{name: "timeout"},
	} {
		t.Run(test.name, func(t *testing.T) {
			mock, err := mockcodex.Start(t)
			require.NoError(t, err)
			directory := shortTempDir(t)
			relay, err := codexrelay.Start(context.Background(), codexrelay.Options{
				SocketPath: directory + "/relay.sock", UpstreamSocketPath: mock.SocketPath,
				Controller:              codexrelay.PassThroughController{},
				LifecycleRequestTimeout: 100 * time.Millisecond,
			})
			require.NoError(t, err)
			t.Cleanup(func() { _ = relay.Close() })
			worker, err := relay.OpenClient(codexrelay.ClientOptions{Role: codexrelay.RoleWorker})
			require.NoError(t, err)
			t.Cleanup(func() { _ = worker.Close() })
			desktop := connectDesktop(t, relay.SocketPath())
			desktop.initialize(t, 1)
			threadID, err := worker.StartThread(context.Background(), mustJSON(map[string]any{
				"cwd": t.TempDir(),
			}))
			require.NoError(t, err)
			var turn struct {
				Turn struct {
					ID string `json:"id"`
				} `json:"turn"`
			}
			require.NoError(t, worker.Call(context.Background(), "turn/start", map[string]any{
				"threadId": threadID,
				"input":    []map[string]string{{"type": "text", "text": "running"}},
			}, &turn))
			desktop.write(t, rpcMessage{ID: rawID(2), Method: "thread/archive",
				Params: mustJSON(map[string]string{"threadId": threadID})})
			if test.disconnect {
				require.NoError(t, desktop.ws.Close())
			} else {
				require.Equal(t, -32000, rpcErrorCode(t, desktop.response(t, rawID(2)).Error))
			}
			time.Sleep(200 * time.Millisecond)
			another := connectDesktop(t, relay.SocketPath())
			another.initialize(t, 10)
			another.write(t, rpcMessage{ID: rawID(11), Method: "turn/steer",
				Params: mustJSON(map[string]any{"threadId": threadID,
					"expectedTurnId": turn.Turn.ID,
					"input":          []map[string]string{{"type": "text", "text": "allowed"}},
				})})
			require.Nil(t, another.response(t, rawID(11)).Error)
			require.True(t, mock.CompleteTurn(threadID, turn.Turn.ID, "done"))
			require.Equal(t, 0, mock.RequestCount("thread/archive"))
		})
	}
}

func TestRelayWaitsForControlRunGateAfterAppServerBecomesIdle(t *testing.T) {
	mock, err := mockcodex.Start(t)
	require.NoError(t, err)
	directory := shortTempDir(t)
	ready := make(chan struct{})
	relay, err := codexrelay.Start(context.Background(), codexrelay.Options{
		SocketPath: directory + "/relay.sock", UpstreamSocketPath: mock.SocketPath,
		Controller: archiveGateController{ready: ready},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = relay.Close() })
	desktop := connectDesktop(t, relay.SocketPath())
	desktop.initialize(t, 1)
	desktop.write(t, rpcMessage{ID: rawID(2), Method: "thread/start",
		Params: mustJSON(map[string]any{"cwd": t.TempDir()})})
	threadID := responseThreadID(t, desktop.response(t, rawID(2)).Result)
	desktop.write(t, rpcMessage{ID: rawID(3), Method: "thread/archive",
		Params: mustJSON(map[string]string{"threadId": threadID})})
	require.Never(t, func() bool {
		return mock.RequestCount("thread/archive") != 0
	}, 200*time.Millisecond, 10*time.Millisecond)
	close(ready)
	require.Nil(t, desktop.response(t, rawID(3)).Error)
	require.Equal(t, 1, mock.RequestCount("thread/archive"))
}

type archiveGateController struct {
	codexrelay.PassThroughController
	ready <-chan struct{}
}

func (c archiveGateController) WaitArchiveReady(ctx context.Context, _ codexrelay.Call,
	_ codexrelay.CallPlan,
) error {
	select {
	case <-c.ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestRelayResumeReadsCurrentUpstreamThreadSettingsAndName(t *testing.T) {
	mock, err := mockcodex.Start(t)
	require.NoError(t, err)
	relay := startRelay(t, mock.SocketPath)
	first := connectDesktop(t, relay.SocketPath())
	second := connectDesktop(t, relay.SocketPath())
	first.initialize(t, 1)
	second.initialize(t, 1)

	first.write(t, rpcMessage{ID: rawID(2), Method: "thread/start",
		Params: mustJSON(map[string]any{"cwd": t.TempDir(), "approvalPolicy": "on-request"})})
	threadID := responseThreadID(t, first.response(t, rawID(2)).Result)
	first.write(t, rpcMessage{ID: rawID(3), Method: "thread/settings/update",
		Params: mustJSON(map[string]any{"threadId": threadID, "approvalPolicy": "never",
			"permissions": ":danger-full-access"})})
	require.Nil(t, first.response(t, rawID(3)).Error)
	first.write(t, rpcMessage{ID: rawID(4), Method: "thread/name/set",
		Params: mustJSON(map[string]any{"threadId": threadID, "name": "最新标题"})})
	require.Nil(t, first.response(t, rawID(4)).Error)

	second.write(t, rpcMessage{ID: rawID(2), Method: "thread/resume",
		Params: mustJSON(map[string]string{"threadId": threadID})})
	resumed := second.response(t, rawID(2))
	require.Nil(t, resumed.Error)
	require.Contains(t, string(resumed.Result), `"approvalPolicy":"never"`)
	require.Contains(t, string(resumed.Result), `"activePermissionProfile":":danger-full-access"`)
	require.Contains(t, string(resumed.Result), `"name":"最新标题"`)

	var upstreamResume bool
	for !upstreamResume {
		select {
		case request := <-mock.Requests():
			if request.Message.Method == "thread/resume" {
				upstreamResume = true
			}
		case <-time.After(time.Second):
			t.Fatal("Relay 没有把第二个 Desktop 的 resume 转发给真实 app-server")
		}
	}
}

func TestRelayKeepsEphemeralDesktopThreadsOutsideController(t *testing.T) {
	mock, err := mockcodex.Start(t)
	require.NoError(t, err)
	directory := shortTempDir(t)
	controller := &recordingController{}
	relay, err := codexrelay.Start(context.Background(), codexrelay.Options{
		SocketPath: directory + "/relay.sock", UpstreamSocketPath: mock.SocketPath,
		Controller: controller,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, relay.Close()) })
	desktop := connectDesktop(t, relay.SocketPath())
	desktop.initialize(t, 1)

	desktop.write(t, rpcMessage{ID: rawID(2), Method: "thread/start",
		Params: mustJSON(map[string]any{"cwd": t.TempDir(), "ephemeral": true})})
	threadID := responseThreadID(t, desktop.response(t, rawID(2)).Result)
	desktop.write(t, rpcMessage{ID: rawID(3), Method: "turn/start",
		Params: mustJSON(map[string]any{"threadId": threadID,
			"input": []map[string]string{{"type": "text", "text": "name helper"}}})})
	require.Nil(t, desktop.response(t, rawID(3)).Error)
	desktop.write(t, rpcMessage{ID: rawID(4), Method: "thread/name/set",
		Params: mustJSON(map[string]string{"threadId": threadID, "name": "helper"})})
	require.Nil(t, desktop.response(t, rawID(4)).Error)
	require.Empty(t, controller.methods())

	desktop.write(t, rpcMessage{ID: rawID(5), Method: "thread/start",
		Params: mustJSON(map[string]any{"cwd": t.TempDir()})})
	require.NotEmpty(t, responseThreadID(t, desktop.response(t, rawID(5)).Result))
	require.Equal(t, []string{"thread/start"}, controller.methods())
}

func TestRelayRoutesDesktopThreadListThroughController(t *testing.T) {
	mock, err := mockcodex.Start(t)
	require.NoError(t, err)
	directory := shortTempDir(t)
	controller := &recordingController{}
	relay, err := codexrelay.Start(context.Background(), codexrelay.Options{
		SocketPath: directory + "/relay.sock", UpstreamSocketPath: mock.SocketPath,
		Controller: controller,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, relay.Close()) })
	desktop := connectDesktop(t, relay.SocketPath())
	desktop.initialize(t, 1)

	desktop.write(t, rpcMessage{ID: rawID(2), Method: "thread/list",
		Params: mustJSON(map[string]any{"archived": false})})
	require.Nil(t, desktop.response(t, rawID(2)).Error)
	require.Equal(t, []string{"thread/list"}, controller.methods())
}

func TestRelayArchivesEphemeralThreadWithoutEnteringController(t *testing.T) {
	mock, err := mockcodex.Start(t)
	require.NoError(t, err)
	directory := shortTempDir(t)
	controller := &recordingController{}
	relay, err := codexrelay.Start(context.Background(), codexrelay.Options{
		SocketPath: directory + "/relay.sock", UpstreamSocketPath: mock.SocketPath,
		Controller: controller,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = relay.Close() })
	desktop := connectDesktop(t, relay.SocketPath())
	desktop.initialize(t, 1)
	desktop.write(t, rpcMessage{ID: rawID(2), Method: "thread/start",
		Params: mustJSON(map[string]any{"cwd": t.TempDir(), "ephemeral": true})})
	threadID := responseThreadID(t, desktop.response(t, rawID(2)).Result)
	desktop.write(t, rpcMessage{ID: rawID(3), Method: "thread/archive",
		Params: mustJSON(map[string]string{"threadId": threadID})})
	require.Nil(t, desktop.response(t, rawID(3)).Error)
	require.Empty(t, controller.methods())
	require.Equal(t, 1, mock.RequestCount("thread/archive"))
}

type recordingController struct {
	mu    sync.Mutex
	calls []string
}

func (c *recordingController) PrepareCall(_ context.Context,
	call codexrelay.Call,
) (codexrelay.CallPlan, error) {
	c.mu.Lock()
	c.calls = append(c.calls, call.Method)
	c.mu.Unlock()
	return codexrelay.CallPlan{Params: call.Params, Forward: true}, nil
}

func (*recordingController) CompleteCall(_ context.Context, _ codexrelay.Call,
	_ codexrelay.CallPlan, result json.RawMessage, cause error,
) (json.RawMessage, error) {
	return result, cause
}

func (*recordingController) ResolveInteractive(_ context.Context, _ codex.ServerRequest,
	answer json.RawMessage, _ codexrelay.Role,
) (bool, json.RawMessage, error) {
	return true, answer, nil
}

func (c *recordingController) methods() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...)
}

func testResponseScope(t *testing.T, raw json.RawMessage) (string, string) {
	t.Helper()
	var value struct {
		ThreadID string `json:"threadId"`
		Turn     struct {
			ID       string `json:"id"`
			ThreadID string `json:"threadId"`
		} `json:"turn"`
	}
	require.NoError(t, json.Unmarshal(raw, &value))
	if value.ThreadID == "" {
		value.ThreadID = value.Turn.ThreadID
	}
	return value.ThreadID, value.Turn.ID
}

func startRelay(t *testing.T, upstream string) *codexrelay.Relay {
	t.Helper()
	directory := shortTempDir(t)
	relay, err := codexrelay.Start(context.Background(), codexrelay.Options{
		SocketPath: directory + "/relay.sock", UpstreamSocketPath: upstream,
		Controller: codexrelay.PassThroughController{},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, relay.Close()) })
	metadata, err := os.Stat(relay.SocketPath())
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o666), metadata.Mode().Perm(),
		"开发容器用户必须能跨 UID/GID 连接 Relay")
	return relay
}

type rpcMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  any             `json:"error,omitempty"`
}

type desktopClient struct{ ws *websocket.Conn }

func connectDesktop(t *testing.T, socketPath string) *desktopClient {
	t.Helper()
	dialer := websocket.Dialer{NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	}}
	ws, response, err := dialer.Dial("ws://localhost/", http.Header{})
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	require.NoError(t, err)
	t.Cleanup(func() { _ = ws.Close() })
	return &desktopClient{ws: ws}
}

func (c *desktopClient) initialize(t *testing.T, id int64) {
	t.Helper()
	c.write(t, rpcMessage{ID: rawID(id), Method: "initialize", Params: mustJSON(map[string]any{
		"clientInfo":   map[string]string{"name": "desktop-test", "version": "1"},
		"capabilities": map[string]any{"experimentalApi": true},
	})})
	require.Nil(t, c.response(t, rawID(id)).Error)
	c.write(t, rpcMessage{Method: "initialized", Params: mustJSON(map[string]any{})})
}

func (c *desktopClient) write(t *testing.T, value rpcMessage) {
	t.Helper()
	require.NoError(t, c.ws.WriteJSON(value))
}

func (c *desktopClient) response(t *testing.T, id json.RawMessage) rpcMessage {
	t.Helper()
	for {
		var message rpcMessage
		require.NoError(t, c.ws.ReadJSON(&message))
		if string(message.ID) == string(id) && message.Method == "" {
			return message
		}
	}
}

func (c *desktopClient) responses(t *testing.T, ids ...json.RawMessage) map[string]rpcMessage {
	t.Helper()
	wanted := make(map[string]bool, len(ids))
	for _, id := range ids {
		wanted[string(id)] = true
	}
	result := make(map[string]rpcMessage, len(ids))
	for len(result) < len(wanted) {
		var message rpcMessage
		require.NoError(t, c.ws.ReadJSON(&message))
		key := string(message.ID)
		if message.Method == "" && wanted[key] {
			result[key] = message
		}
	}
	return result
}

func (c *desktopClient) serverRequest(t *testing.T, method string) rpcMessage {
	t.Helper()
	for {
		var message rpcMessage
		require.NoError(t, c.ws.ReadJSON(&message))
		if len(message.ID) > 0 && message.Method == method {
			return message
		}
	}
}

func (c *desktopClient) notification(t *testing.T, method string) rpcMessage {
	t.Helper()
	for {
		var message rpcMessage
		require.NoError(t, c.ws.ReadJSON(&message))
		if len(message.ID) == 0 && message.Method == method {
			return message
		}
	}
}

func (c *desktopClient) expectNoServerRequest(t *testing.T, wait time.Duration) {
	t.Helper()
	require.NoError(t, c.ws.SetReadDeadline(time.Now().Add(wait)))
	defer func() { _ = c.ws.SetReadDeadline(time.Time{}) }()
	for {
		var message rpcMessage
		err := c.ws.ReadJSON(&message)
		if err != nil {
			return
		}
		if len(message.ID) > 0 && message.Method != "" {
			t.Fatalf("Desktop 不应收到 Server Request %s", message.Method)
		}
	}
}

func responseThreadID(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var value struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	require.NoError(t, json.Unmarshal(raw, &value))
	require.NotEmpty(t, value.Thread.ID)
	return value.Thread.ID
}

func eventThreadID(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var value struct {
		ThreadID string `json:"threadId"`
		Thread   struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	require.NoError(t, json.Unmarshal(raw, &value))
	if value.ThreadID != "" {
		return value.ThreadID
	}
	return value.Thread.ID
}

func receiveEvent(t *testing.T, events <-chan codex.Event) codex.Event {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(3 * time.Second):
		t.Fatal("等待 Relay 事件超时")
		return codex.Event{}
	}
}

func rawID(value int64) json.RawMessage { return json.RawMessage(strconv.FormatInt(value, 10)) }

func rpcErrorCode(t *testing.T, value any) int {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	var response struct {
		Code int `json:"code"`
	}
	require.NoError(t, json.Unmarshal(encoded, &response))
	return response.Code
}

func mustJSON(value any) json.RawMessage {
	result, _ := json.Marshal(value)
	return result
}

func shortTempDir(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "tyrs-relay-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(directory)) })
	return directory
}
