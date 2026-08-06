//go:build integration

package protocol

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/appserverhub"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/ports"
	"github.com/stretchr/testify/require"
)

func TestUserJourneyPhoneSteersDesktopTurnAndAnotherClientStopsIt(t *testing.T) {
	firstStarted := make(chan struct{})
	steerStarted := make(chan struct{})
	steerCanceled := make(chan struct{}, 1)
	releaseFirst := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseFirst) }) })
	requestBodies := make(chan string, 4)
	var requestNumber atomic.Int32
	responses := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter,
		request *http.Request,
	) {
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		requestBodies <- string(body)
		number := requestNumber.Add(1)
		switch number {
		case 1:
			close(firstStarted)
			select {
			case <-releaseFirst:
			case <-request.Context().Done():
				return
			}
		case 2:
			close(steerStarted)
			<-request.Context().Done()
			steerCanceled <- struct{}{}
			return
		}
		id := fmt.Sprintf("cross-client-response-%d", number)
		response.Header().Set("Content-Type", "text/event-stream")
		response.Header().Set("Cache-Control", "no-cache")
		_, _ = fmt.Fprint(response, sse(
			map[string]any{"type": "response.created", "response": map[string]any{"id": id}},
			map[string]any{"type": "response.output_item.done", "item": map[string]any{
				"type": "message", "role": "assistant", "id": "message-" + id,
				"content": []map[string]any{{"type": "output_text", "text": "done"}},
			}}, completedResponse(id)))
	}))
	t.Cleanup(responses.Close)

	hub, workspace := startRealHub(t, responses.URL, nil)
	worker, err := hub.OpenClient(appserverhub.ClientOptions{Role: appserverhub.RoleWorker})
	require.NoError(t, err)
	t.Cleanup(func() { _ = worker.Close() })
	firstDesktop := connectRealDesktop(t, hub)
	secondDesktop := connectRealDesktop(t, hub)

	threadID := startRealThread(t, firstDesktop, workspace)
	workerEvents := worker.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	firstEvents := firstDesktop.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	secondEvents := secondDesktop.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	t.Cleanup(workerEvents.Close)
	t.Cleanup(firstEvents.Close)
	t.Cleanup(secondEvents.Close)

	turnID := startRealTurn(t, firstDesktop, threadID, "Desktop 正在处理一个较长任务")
	select {
	case <-firstStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("Desktop Turn 没有到达 mock LLM")
	}
	require.NoError(t, codex.NewRuntime(worker).SteerTurn(context.Background(), threadID,
		turnID, ports.TurnInput{Text: "手机补充：也检查异常恢复"}))
	releaseOnce.Do(func() { close(releaseFirst) })
	select {
	case <-steerStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("手机追加消息没有形成真实 Codex steer 请求")
	}
	require.NoError(t, secondDesktop.Call(context.Background(), "turn/interrupt",
		map[string]any{"threadId": threadID, "turnId": turnID}, nil))
	select {
	case <-steerCanceled:
	case <-time.After(10 * time.Second):
		t.Fatal("另一客户端停止后，活动 LLM 请求没有取消")
	}
	for _, events := range []<-chan codex.Event{
		workerEvents.Events(), firstEvents.Events(), secondEvents.Events(),
	} {
		require.NotEqual(t, "completed", waitForHubTurnStatus(t, events, threadID, turnID))
	}

	recoveryTurn := startRealTurn(t, worker, threadID, "手机重新发送一个正常任务")
	for _, events := range []<-chan codex.Event{
		workerEvents.Events(), firstEvents.Events(), secondEvents.Events(),
	} {
		require.Equal(t, "completed", waitForHubTurnStatus(t, events, threadID, recoveryTurn))
	}
	transcript := <-requestBodies + "\n" + <-requestBodies + "\n" + <-requestBodies
	require.Contains(t, transcript, "手机补充")
	require.Contains(t, transcript, "手机重新发送")
	require.Equal(t, int64(1), hub.Stats().UpstreamConnections)
}

func TestUserJourneyOneDesktopUnsubscribesWhilePhoneAndOtherDesktopContinue(t *testing.T) {
	var responseNumber atomic.Int32
	responses := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter,
		_ *http.Request,
	) {
		number := responseNumber.Add(1)
		id := fmt.Sprintf("unsubscribe-response-%d", number)
		response.Header().Set("Content-Type", "text/event-stream")
		response.Header().Set("Cache-Control", "no-cache")
		_, _ = fmt.Fprint(response, sse(
			map[string]any{"type": "response.created", "response": map[string]any{"id": id}},
			map[string]any{"type": "response.output_item.done", "item": map[string]any{
				"type": "message", "role": "assistant", "id": "message-" + id,
				"content": []map[string]any{{"type": "output_text", "text": "done"}},
			}}, completedResponse(id)))
	}))
	t.Cleanup(responses.Close)

	hub, workspace := startRealHub(t, responses.URL, nil)
	worker, err := hub.OpenClient(appserverhub.ClientOptions{Role: appserverhub.RoleWorker})
	require.NoError(t, err)
	t.Cleanup(func() { _ = worker.Close() })
	firstDesktop := connectRealDesktop(t, hub)
	secondDesktop := connectRealDesktop(t, hub)
	threadID, err := worker.StartThread(context.Background(), mustRealJSON(map[string]any{
		"cwd": workspace, "model": "mock-model", "approvalPolicy": "never",
		"sandbox": "read-only",
	}))
	require.NoError(t, err)
	firstEvents := firstDesktop.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	secondEvents := secondDesktop.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	workerEvents := worker.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	t.Cleanup(firstEvents.Close)
	t.Cleanup(secondEvents.Close)
	t.Cleanup(workerEvents.Close)

	var unsubscribe struct {
		Status string `json:"status"`
	}
	require.NoError(t, firstDesktop.Call(context.Background(), "thread/unsubscribe",
		map[string]any{"threadId": threadID}, &unsubscribe))
	require.Equal(t, "unsubscribed", unsubscribe.Status)
	phoneTurn := startRealTurn(t, worker, threadID, "手机发送，第一台电脑已离开会话")
	require.Equal(t, "completed", waitForHubTurnStatus(t, workerEvents.Events(), threadID, phoneTurn))
	require.Equal(t, "completed", waitForHubTurnStatus(t, secondEvents.Events(), threadID, phoneTurn))
	assertNoTurnEvent(t, firstEvents.Events(), threadID, phoneTurn, 250*time.Millisecond)

	var resumed any
	require.NoError(t, firstDesktop.Call(context.Background(), "thread/resume",
		map[string]any{"threadId": threadID}, &resumed))
	rejoinedTurn := startRealTurn(t, secondDesktop, threadID, "第二台电脑发送，第一台已重新加入")
	for _, events := range []<-chan codex.Event{
		workerEvents.Events(), firstEvents.Events(), secondEvents.Events(),
	} {
		require.Equal(t, "completed", waitForHubTurnStatus(t, events, threadID, rejoinedTurn))
	}
	require.Equal(t, int32(2), responseNumber.Load())
}

func TestUserJourneyWorkerHotReplacementKeepsDesktopToolCallRunning(t *testing.T) {
	var responseNumber atomic.Int32
	responses := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter,
		_ *http.Request,
	) {
		number := responseNumber.Add(1)
		response.Header().Set("Content-Type", "text/event-stream")
		response.Header().Set("Cache-Control", "no-cache")
		if number == 1 {
			_, _ = fmt.Fprint(response, sse(
				map[string]any{"type": "response.created", "response": map[string]any{
					"id": "handoff-response-1"}},
				map[string]any{"type": "response.output_item.done", "item": map[string]any{
					"type": "function_call", "call_id": "handoff-call", "namespace": "github",
					"name": "echo", "arguments": `{"message":"after replacement"}`,
				}}, completedResponse("handoff-response-1")))
			return
		}
		_, _ = fmt.Fprint(response, sse(
			map[string]any{"type": "response.created", "response": map[string]any{
				"id": "handoff-response-2"}},
			map[string]any{"type": "response.output_item.done", "item": map[string]any{
				"type": "message", "role": "assistant", "id": "handoff-message",
				"content": []map[string]any{{"type": "output_text", "text": "done"}},
			}}, completedResponse("handoff-response-2")))
	}))
	t.Cleanup(responses.Close)

	hub, workspace := startRealHub(t, responses.URL, nil)
	oldCalls := make(chan codex.ServerRequest, 1)
	oldWorker, err := hub.OpenClient(appserverhub.ClientOptions{Role: appserverhub.RoleWorker,
		ServerRequestHandler: func(_ context.Context, request codex.ServerRequest) (any, error) {
			oldCalls <- request
			return codex.TextToolResult("old-worker", true), nil
		}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = oldWorker.Close() })
	desktop := connectRealDesktop(t, hub)
	threadID, err := oldWorker.StartThread(context.Background(), mustRealJSON(map[string]any{
		"cwd": workspace, "model": "mock-model", "approvalPolicy": "never",
		"sandbox": "read-only",
		"dynamicTools": []map[string]any{{"type": "namespace", "name": "github",
			"description": "handoff tools", "tools": []map[string]any{{
				"type": "function", "name": "echo", "description": "echo",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
					"message": map[string]string{"type": "string"}},
					"required": []string{"message"}, "additionalProperties": false},
			}}}},
	}))
	require.NoError(t, err)

	newCalls := make(chan codex.ServerRequest, 1)
	newWorker, err := hub.OpenClient(appserverhub.ClientOptions{Role: appserverhub.RoleWorker,
		ServerRequestHandler: func(_ context.Context, request codex.ServerRequest) (any, error) {
			newCalls <- request
			return codex.TextToolResult("new-worker", true), nil
		}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = newWorker.Close() })
	desktopEvents := desktop.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	t.Cleanup(desktopEvents.Close)
	turnID := startRealTurn(t, desktop, threadID, "部署切换后调用 echo 工具")
	select {
	case request := <-newCalls:
		require.Equal(t, "item/tool/call", request.Method)
	case <-time.After(10 * time.Second):
		t.Fatal("新 Worker 没有接管既有 Thread 的工具调用")
	}
	select {
	case request := <-oldCalls:
		t.Fatalf("旧 Worker 不应再收到工具调用: %s", request.Method)
	case <-time.After(200 * time.Millisecond):
	}
	require.Equal(t, "completed", waitForHubTurnStatus(t, desktopEvents.Events(), threadID, turnID))
	require.Equal(t, int64(1), hub.Stats().WorkerConnections)
	require.Equal(t, int32(2), responseNumber.Load())
}

func TestUserJourneyPhoneArchiveWaitsForDesktopTurn(t *testing.T) {
	requestStarted := make(chan struct{})
	requestCanceled := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	responses := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter,
		request *http.Request,
	) {
		close(requestStarted)
		select {
		case <-release:
		case <-request.Context().Done():
			requestCanceled <- struct{}{}
			return
		}
		response.Header().Set("Content-Type", "text/event-stream")
		response.Header().Set("Cache-Control", "no-cache")
		_, _ = fmt.Fprint(response, sse(
			map[string]any{"type": "response.created", "response": map[string]any{
				"id": "phone-archive-response"}},
			map[string]any{"type": "response.output_item.done", "item": map[string]any{
				"type": "message", "role": "assistant", "id": "phone-archive-message",
				"content": []map[string]any{{"type": "output_text", "text": "done"}},
			}}, completedResponse("phone-archive-response")))
	}))
	t.Cleanup(responses.Close)
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	hub, workspace := startRealHub(t, responses.URL, nil)
	worker, err := hub.OpenClient(appserverhub.ClientOptions{Role: appserverhub.RoleWorker})
	require.NoError(t, err)
	t.Cleanup(func() { _ = worker.Close() })
	desktop := connectRealDesktop(t, hub)
	secondDesktop := connectRealDesktop(t, hub)
	threadID := startRealThread(t, desktop, workspace)
	workerEvents := worker.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	desktopEvents := desktop.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	secondDesktopEvents := secondDesktop.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	t.Cleanup(workerEvents.Close)
	t.Cleanup(desktopEvents.Close)
	t.Cleanup(secondDesktopEvents.Close)
	turnID := startRealTurn(t, desktop, threadID, "电脑正在执行，手机现在请求归档")
	select {
	case <-requestStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("Desktop Turn 没有到达 mock LLM")
	}
	archiveDone := make(chan error, 2)
	go func() {
		archiveDone <- worker.Call(context.Background(), "thread/archive",
			map[string]any{"threadId": threadID}, nil)
	}()
	go func() {
		archiveDone <- secondDesktop.Call(context.Background(), "thread/archive",
			map[string]any{"threadId": threadID}, nil)
	}()
	select {
	case err := <-archiveDone:
		t.Fatalf("活动 Turn 完成前手机归档不应返回: %v", err)
	case <-time.After(250 * time.Millisecond):
	}
	select {
	case <-requestCanceled:
		t.Fatal("手机归档不应取消电脑上的活动 Turn")
	default:
	}
	releaseOnce.Do(func() { close(release) })
	require.Equal(t, "completed", waitForHubTurnStatus(t, workerEvents.Events(), threadID, turnID))
	require.Equal(t, "completed", waitForHubTurnStatus(t, desktopEvents.Events(), threadID, turnID))
	for range 2 {
		select {
		case err := <-archiveDone:
			require.NoError(t, err)
		case <-time.After(10 * time.Second):
			t.Fatal("Turn 完成后并发归档没有结束")
		}
	}
	waitForLifecycleEvent(t, workerEvents.Events(), "thread/archived", threadID)
	waitForLifecycleEvent(t, desktopEvents.Events(), "thread/archived", threadID)
	waitForLifecycleEvent(t, secondDesktopEvents.Events(), "thread/archived", threadID)
}

func connectRealDesktop(t *testing.T, hub *appserverhub.Hub) *codex.SocketClient {
	t.Helper()
	client, err := codex.ConnectSocket(context.Background(), codex.SocketClientOptions{
		SocketPath: hub.SocketPath(), RequestTimeout: 30 * time.Second,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func startRealThread(t *testing.T, client *codex.SocketClient, workspace string) string {
	t.Helper()
	var result struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	require.NoError(t, client.Call(context.Background(), "thread/start", map[string]any{
		"cwd": workspace, "model": "mock-model", "approvalPolicy": "never",
		"sandbox": "read-only",
	}, &result))
	require.NotEmpty(t, result.Thread.ID)
	return result.Thread.ID
}

type realTurnStarter interface {
	Call(context.Context, string, any, any) error
}

func startRealTurn(t *testing.T, client realTurnStarter, threadID, text string) string {
	t.Helper()
	var result struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	require.NoError(t, client.Call(context.Background(), "turn/start", map[string]any{
		"threadId": threadID,
		"input":    []map[string]any{{"type": "text", "text": text, "textElements": []any{}}},
	}, &result))
	require.NotEmpty(t, result.Turn.ID)
	return result.Turn.ID
}
