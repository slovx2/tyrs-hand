//go:build integration

package protocol

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/appserverhub"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/ports"
	"github.com/stretchr/testify/require"
)

func TestP0ConcurrentThreadsKeepSteerInterruptAndEventsIsolated(t *testing.T) {
	aInitialStarted := make(chan struct{})
	bInitialStarted := make(chan struct{})
	aSteerStarted := make(chan struct{})
	aSteerCanceled := make(chan struct{}, 1)
	releaseA := make(chan struct{})
	releaseB := make(chan struct{})
	var releaseAOnce sync.Once
	var releaseBOnce sync.Once
	t.Cleanup(func() {
		releaseAOnce.Do(func() { close(releaseA) })
		releaseBOnce.Do(func() { close(releaseB) })
	})
	var responseNumber atomic.Int32
	responses := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter,
		request *http.Request,
	) {
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		content := string(body)
		switch {
		case strings.Contains(content, "手机补充 A"):
			close(aSteerStarted)
			<-request.Context().Done()
			aSteerCanceled <- struct{}{}
			return
		case strings.Contains(content, "会话 A 初始任务"):
			close(aInitialStarted)
			select {
			case <-releaseA:
			case <-request.Context().Done():
				return
			}
		case strings.Contains(content, "会话 B 初始任务"):
			close(bInitialStarted)
			select {
			case <-releaseB:
			case <-request.Context().Done():
				return
			}
		}
		writeP0CompletedResponse(response,
			fmt.Sprintf("isolated-response-%d", responseNumber.Add(1)))
	}))
	t.Cleanup(responses.Close)

	hub, workspace := startRealHub(t, responses.URL, nil)
	worker, err := hub.OpenClient(appserverhub.ClientOptions{Role: appserverhub.RoleWorker})
	require.NoError(t, err)
	t.Cleanup(func() { _ = worker.Close() })
	desktopA := connectRealDesktop(t, hub)
	desktopB := connectRealDesktop(t, hub)
	threadA := startRealThread(t, desktopA, workspace)
	threadB := startRealThread(t, desktopB, workspace)
	desktopAEvents := desktopA.Subscribe(codex.ThreadFilter{ThreadID: threadA})
	desktopBEvents := desktopB.Subscribe(codex.ThreadFilter{ThreadID: threadB})
	workerAEvents := worker.Subscribe(codex.ThreadFilter{ThreadID: threadA})
	workerBEvents := worker.Subscribe(codex.ThreadFilter{ThreadID: threadB})
	t.Cleanup(desktopAEvents.Close)
	t.Cleanup(desktopBEvents.Close)
	t.Cleanup(workerAEvents.Close)
	t.Cleanup(workerBEvents.Close)

	turnA := startRealTurn(t, desktopA, threadA, "会话 A 初始任务")
	turnB := startRealTurn(t, desktopB, threadB, "会话 B 初始任务")
	waitForP0Signal(t, aInitialStarted, "会话 A 没有进入活动 Turn")
	waitForP0Signal(t, bInitialStarted, "会话 B 没有与 A 并发运行")
	require.NoError(t, codex.NewRuntime(worker).SteerTurn(context.Background(), threadA,
		turnA, ports.TurnInput{Text: "手机补充 A：同时检查异常恢复"}))
	releaseAOnce.Do(func() { close(releaseA) })
	waitForP0Signal(t, aSteerStarted, "Worker steer 没有进入会话 A 的活动 Turn")
	require.NoError(t, desktopA.Call(context.Background(), "turn/interrupt",
		map[string]any{"threadId": threadA, "turnId": turnA}, nil))
	waitForP0Signal(t, aSteerCanceled, "interrupt 没有取消会话 A 的 steer 请求")
	releaseBOnce.Do(func() { close(releaseB) })

	statusA := waitForP0OwnTurnWithoutForeignThread(t, desktopAEvents.Events(),
		threadA, turnA, threadB)
	statusB := waitForP0OwnTurnWithoutForeignThread(t, desktopBEvents.Events(),
		threadB, turnB, threadA)
	require.NotEqual(t, "completed", statusA)
	require.Equal(t, "completed", statusB)
	require.NotEqual(t, "completed",
		waitForHubTurnStatus(t, workerAEvents.Events(), threadA, turnA))
	require.Equal(t, "completed",
		waitForHubTurnStatus(t, workerBEvents.Events(), threadB, turnB))
	require.Equal(t, int64(1), hub.Stats().UpstreamConnections)
}

func TestP0DesktopDisconnectDuringTurnDoesNotBlockRemainingClients(t *testing.T) {
	firstRequestStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseFirst) }) })
	var responseNumber atomic.Int32
	responses := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter,
		request *http.Request,
	) {
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		number := responseNumber.Add(1)
		if number == 1 {
			require.Contains(t, string(body), "桌面断线中的任务")
			close(firstRequestStarted)
			select {
			case <-releaseFirst:
			case <-request.Context().Done():
				t.Fatal("桌面断线不应取消共享的活动 Turn")
			}
		}
		writeP0CompletedResponse(response,
			fmt.Sprintf("disconnect-response-%d", number))
	}))
	t.Cleanup(responses.Close)

	hub, workspace := startRealHub(t, responses.URL, nil)
	worker, err := hub.OpenClient(appserverhub.ClientOptions{Role: appserverhub.RoleWorker})
	require.NoError(t, err)
	t.Cleanup(func() { _ = worker.Close() })
	leavingDesktop := connectRealDesktop(t, hub)
	remainingDesktop := connectRealDesktop(t, hub)
	threadID := startRealThread(t, leavingDesktop, workspace)
	workerEvents := worker.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	remainingEvents := remainingDesktop.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	t.Cleanup(workerEvents.Close)
	t.Cleanup(remainingEvents.Close)

	firstTurn := startRealTurn(t, leavingDesktop, threadID, "桌面断线中的任务")
	waitForP0Signal(t, firstRequestStarted, "断线前任务没有进入活动 Turn")
	require.NoError(t, leavingDesktop.Close())
	require.Eventually(t, func() bool {
		return hub.Stats().DesktopConnections == 1
	}, 5*time.Second, 20*time.Millisecond)
	releaseOnce.Do(func() { close(releaseFirst) })
	require.Equal(t, "completed",
		waitForHubTurnStatus(t, workerEvents.Events(), threadID, firstTurn))
	require.Equal(t, "completed",
		waitForHubTurnStatus(t, remainingEvents.Events(), threadID, firstTurn))

	nextTurn := startRealTurn(t, remainingDesktop, threadID, "其余客户端继续下一项任务")
	require.Equal(t, "completed",
		waitForHubTurnStatus(t, workerEvents.Events(), threadID, nextTurn))
	require.Equal(t, "completed",
		waitForHubTurnStatus(t, remainingEvents.Events(), threadID, nextTurn))
	require.Equal(t, int32(2), responseNumber.Load())
	require.Equal(t, int64(1), hub.Stats().UpstreamConnections)
}

func writeP0CompletedResponse(response http.ResponseWriter, id string) {
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("Cache-Control", "no-cache")
	_, _ = fmt.Fprint(response, sse(
		map[string]any{"type": "response.created", "response": map[string]any{"id": id}},
		map[string]any{"type": "response.output_item.done", "item": map[string]any{
			"type": "message", "role": "assistant", "id": "message-" + id,
			"content": []map[string]any{{"type": "output_text", "text": "done"}},
		}}, completedResponse(id)))
}

func waitForP0Signal(t *testing.T, signal <-chan struct{}, failure string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(10 * time.Second):
		t.Fatal(failure)
	}
}

func waitForP0OwnTurnWithoutForeignThread(t *testing.T, events <-chan codex.Event,
	threadID, turnID, foreignThreadID string,
) string {
	t.Helper()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event, ok := <-events:
			require.True(t, ok, "等待 Turn 终态时客户端断开")
			eventThreadID, eventTurnID := protocolEventScope(event.Params)
			if eventThreadID == foreignThreadID {
				t.Fatalf("客户端收到另一会话事件 %s: %s", event.Method, event.Params)
			}
			if event.Method != "turn/completed" || eventThreadID != threadID ||
				eventTurnID != turnID {
				continue
			}
			var params struct {
				Turn struct {
					Status string `json:"status"`
				} `json:"turn"`
			}
			require.NoError(t, json.Unmarshal(event.Params, &params))
			return params.Turn.Status
		case <-timer.C:
			t.Fatal("等待客户端 Turn 终态超时")
		}
	}
}
