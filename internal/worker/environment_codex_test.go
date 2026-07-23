package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/codexrelay"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/devcontainer"
	"github.com/slovx2/tyrs-hand/internal/testutil/mockcodex"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestEnvironmentCodexObserverSubmitsThreadNamesFromRelay(t *testing.T) {
	mock, err := mockcodex.Start(t)
	require.NoError(t, err)
	root, err := os.MkdirTemp("/tmp", "tyrs-env-codex-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(root)) })
	relay, err := codexrelay.Start(context.Background(), codexrelay.Options{
		SocketPath: filepath.Join(root, "relay.sock"), UpstreamSocketPath: mock.SocketPath,
		Controller: codexrelay.PassThroughController{},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, relay.Close()) })
	client, err := relay.OpenClient(codexrelay.ClientOptions{Role: codexrelay.RoleWorker})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	received := make(chan workerprotocol.ThreadMetadataRequest, 3)
	control := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter,
		request *http.Request,
	) {
		require.Equal(t, "/worker/v1/thread-metadata-events", request.URL.Path)
		require.Equal(t, "Bearer worker-credential", request.Header.Get("Authorization"))
		var input workerprotocol.ThreadMetadataRequest
		require.NoError(t, json.NewDecoder(request.Body).Decode(&input))
		received <- input
		response.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(control.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	environmentID := uuid.New()
	processor := &RemoteProcessor{
		cfg:    config.Config{ControlTimeout: time.Second},
		client: workerprotocol.NewClient(control.URL, "worker-credential", time.Second),
		logger: zap.NewNop(),
	}
	environment := &environmentCodex{
		client: client, processor: processor, generation: 37,
		runtime:        devcontainer.Runtime{EnvironmentID: environmentID},
		metadataEvents: client.Subscribe(codex.ThreadFilter{}),
	}
	t.Cleanup(environment.metadataEvents.Close)
	go environment.observeMetadata(ctx)

	threadID, err := client.StartThread(context.Background(),
		json.RawMessage(`{"cwd":"/workspace"}`))
	require.NoError(t, err)
	mock.Emit(threadID, "thread/settings/updated", map[string]any{
		"threadId": threadID, "threadSettings": map[string]any{
			"model": "gpt-5.6-sol", "effort": "ultra", "serviceTier": "priority",
		},
	})
	mock.Emit(threadID, "thread/name/updated", map[string]any{
		"threadId": threadID, "threadName": "第一个标题",
	})
	mock.Emit(threadID, "thread/name/updated", map[string]any{
		"threadId": threadID, "threadName": "第二个标题",
	})

	select {
	case request := <-received:
		require.Equal(t, "settings", request.Events[0].Kind)
		require.Equal(t, int64(1), request.Events[0].Sequence)
		require.Equal(t, "gpt-5.6-sol", request.Events[0].Model)
		require.Equal(t, "ultra", request.Events[0].ReasoningEffort)
		require.Equal(t, "priority", request.Events[0].ServiceTier)
	case <-time.After(3 * time.Second):
		t.Fatal("Worker 没有提交 Relay 广播的 Thread 设置")
	}
	for sequence, name := range []string{"第一个标题", "第二个标题"} {
		select {
		case request := <-received:
			require.Equal(t, environmentID, request.EnvironmentID)
			require.Equal(t, int64(37), request.Generation)
			require.Len(t, request.Events, 1)
			require.Equal(t, threadID, request.Events[0].ThreadID)
			require.Equal(t, int64(sequence+1), request.Events[0].Sequence)
			require.Equal(t, name, request.Events[0].Name)
		case <-time.After(3 * time.Second):
			t.Fatal("Worker 没有提交 Relay 广播的 Thread 名称")
		}
	}
}

func TestWorkerAppliesLunaThreadNameAndRelayBroadcastsToEveryDesktop(t *testing.T) {
	mock, err := mockcodex.Start(t)
	require.NoError(t, err)
	root, err := os.MkdirTemp("/tmp", "tyrs-luna-name-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(root)) })
	relay, err := codexrelay.Start(context.Background(), codexrelay.Options{
		SocketPath: filepath.Join(root, "relay.sock"), UpstreamSocketPath: mock.SocketPath,
		Controller: codexrelay.PassThroughController{},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, relay.Close()) })
	workerClient, err := relay.OpenClient(codexrelay.ClientOptions{Role: codexrelay.RoleWorker})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, workerClient.Close()) })
	threadID, err := workerClient.StartThread(context.Background(),
		json.RawMessage(`{"cwd":"/workspace"}`))
	require.NoError(t, err)

	first, err := codex.ConnectSocket(context.Background(), codex.SocketClientOptions{
		SocketPath: relay.SocketPath(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, first.Close()) })
	second, err := codex.ConnectSocket(context.Background(), codex.SocketClientOptions{
		SocketPath: relay.SocketPath(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, second.Close()) })
	for _, desktop := range []*codex.SocketClient{first, second} {
		require.NoError(t, desktop.Call(context.Background(), "thread/resume",
			map[string]string{"threadId": threadID}, nil))
	}
	firstEvents := first.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	secondEvents := second.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	t.Cleanup(firstEvents.Close)
	t.Cleanup(secondEvents.Close)

	environmentID, controlID := uuid.New(), uuid.New()
	acknowledged := make(chan workerprotocol.ThreadNameAckRequest, 1)
	control := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter,
		request *http.Request,
	) {
		switch {
		case request.Method == http.MethodGet &&
			request.URL.Path == "/worker/v1/thread-name-updates":
			_ = json.NewEncoder(response).Encode([]workerprotocol.ThreadNameUpdate{{
				ControlID: controlID, EnvironmentID: environmentID, ThreadID: threadID,
				Name: "Luna 统一标题", Revision: 9,
			}})
		case request.Method == http.MethodPost &&
			request.URL.Path == "/worker/v1/thread-name-updates/"+controlID.String()+"/ack":
			var input workerprotocol.ThreadNameAckRequest
			require.NoError(t, json.NewDecoder(request.Body).Decode(&input))
			acknowledged <- input
			response.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(control.Close)

	processor := &RemoteProcessor{
		cfg:    config.Config{ControlTimeout: time.Second},
		client: workerprotocol.NewClient(control.URL, "worker-credential", time.Second),
		logger: zap.NewNop(),
	}
	processor.environments = &environmentCodexRegistry{
		entries: map[uuid.UUID]*environmentCodex{
			environmentID: {client: workerClient},
		},
	}
	require.NoError(t, processor.applyPendingThreadNames(context.Background()))
	for _, events := range []<-chan codex.Event{firstEvents.Events(), secondEvents.Events()} {
		select {
		case event := <-events:
			require.Equal(t, "thread/name/updated", event.Method)
			require.Contains(t, string(event.Params), "Luna 统一标题")
		case <-time.After(3 * time.Second):
			t.Fatal("Desktop 没有收到 Luna 标题更新")
		}
	}
	select {
	case ack := <-acknowledged:
		require.Equal(t, environmentID, ack.EnvironmentID)
		require.Equal(t, int64(9), ack.Revision)
		require.Empty(t, ack.Error)
	case <-time.After(3 * time.Second):
		t.Fatal("Worker 没有确认 Luna 标题 revision")
	}
}
