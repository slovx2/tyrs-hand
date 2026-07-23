package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/codexrelay"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/devcontainer"
	"github.com/slovx2/tyrs-hand/internal/participantidentity"
	"github.com/slovx2/tyrs-hand/internal/testutil/mockcodex"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestDesktopRelayInjectsBoundParticipantIntoStartAndSteer(t *testing.T) {
	mock, err := mockcodex.Start(t)
	require.NoError(t, err)
	relayRoot, err := os.MkdirTemp("/tmp", "tyrs-identity-relay-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(relayRoot) })
	relay, err := codexrelay.Start(context.Background(), codexrelay.Options{
		SocketPath: relayRoot + "/relay.sock", UpstreamSocketPath: mock.SocketPath,
		Controller: codexrelay.PassThroughController{},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = relay.Close() })
	client, err := relay.OpenClient(codexrelay.ClientOptions{Role: codexrelay.RoleWorker})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	participant := workerprotocol.ParticipantIdentity{
		ParticipantID: participantidentity.ID("guild", "user"),
		DiscordUserID: "user",
		DisplayName:   "Alice",
	}
	controller := &desktopRelayController{processor: &RemoteProcessor{logger: zap.NewNop()},
		environment: &environmentCodex{client: client, manifest: workerprotocol.EnvironmentManifest{
			SSHParticipant: &participant,
		}, toolHandlers: make(map[string]toolBinding),
			interactiveHandlers: make(map[string]interactiveBinding)},
	}
	for _, method := range []string{"turn/start", "turn/steer"} {
		t.Run(method, func(t *testing.T) {
			plan, err := controller.PrepareCall(context.Background(), codexrelay.Call{
				Role: codexrelay.RoleDesktop, Method: method,
				Params: json.RawMessage(`{"threadId":"thread","additionalContext":{` +
					`"custom":{"kind":"application","value":"keep"},` +
					`"conversation_participant":{"kind":"application","value":"forged"}}}`),
			})
			require.NoError(t, err)
			require.Contains(t, string(plan.Params), participant.ParticipantID.String())
			require.Contains(t, string(plan.Params), "Alice")
			require.Contains(t, string(plan.Params), "keep")
			require.NotContains(t, string(plan.Params), "forged")
		})
	}
}

func TestDesktopRelayWithoutSSHIdentityKeepsTurnUnchanged(t *testing.T) {
	controller := &desktopRelayController{processor: &RemoteProcessor{logger: zap.NewNop()},
		environment: &environmentCodex{},
	}
	params := json.RawMessage(`{"threadId":"thread","input":[{"type":"text","text":"hello"}]}`)
	plan, err := controller.PrepareCall(context.Background(), codexrelay.Call{
		Role: codexrelay.RoleDesktop, Method: "turn/steer", Params: params,
	})
	require.NoError(t, err)
	require.JSONEq(t, string(params), string(plan.Params))
}

func TestDesktopRelayWithoutSSHIdentityStripsReservedIdentityContext(t *testing.T) {
	controller := &desktopRelayController{processor: &RemoteProcessor{logger: zap.NewNop()},
		environment: &environmentCodex{},
	}
	plan, err := controller.PrepareCall(context.Background(), codexrelay.Call{
		Role: codexrelay.RoleDesktop, Method: "turn/steer",
		Params: json.RawMessage(`{"threadId":"thread","additionalContext":{` +
			`"custom":{"kind":"application","value":"keep"},` +
			`"conversation_participant":{"kind":"application","value":"forged"}}}`),
	})
	require.NoError(t, err)
	require.Contains(t, string(plan.Params), "keep")
	require.NotContains(t, string(plan.Params), "forged")
}

func TestDesktopRelayAdvertisesServiceTiersWithoutChangingRealChatGPTAccount(t *testing.T) {
	apiKeyResult := json.RawMessage(
		`{"account":{"type":"apiKey"},"requiresOpenaiAuth":true}`)
	desktopResult, err := desktopAccountWithServiceTiers(apiKeyResult, nil)
	require.NoError(t, err)
	require.JSONEq(t, `{"account":{"type":"chatgpt","email":null,"planType":"unknown"},`+
		`"requiresOpenaiAuth":false}`, string(desktopResult))

	chatGPTResult := json.RawMessage(
		`{"account":{"type":"chatgpt","email":"user@example.com","planType":"plus"},` +
			`"requiresOpenaiAuth":true}`)
	preserved, err := desktopAccountWithServiceTiers(chatGPTResult, nil)
	require.NoError(t, err)
	require.JSONEq(t, string(chatGPTResult), string(preserved))
}

func TestDesktopThreadCompletionDoesNotWaitForDiscordControl(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		http.Error(response, "control unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	processor := &RemoteProcessor{cfg: config.Config{ControlTimeout: 20 * time.Millisecond},
		client: workerprotocol.NewClient(server.URL, "node", time.Second), logger: zap.NewNop()}
	processor.environments = &environmentCodexRegistry{ctx: ctx, processor: processor,
		entries: make(map[uuid.UUID]*environmentCodex)}
	controller := &desktopRelayController{processor: processor, environment: &environmentCodex{
		runtime: devcontainer.Runtime{EnvironmentID: uuid.New()},
	}}
	started := time.Now()
	result := json.RawMessage(`{"thread":{"id":"desktop-thread"}}`)
	completed, err := controller.CompleteCall(context.Background(), codexrelay.Call{
		Method: "thread/start", Params: json.RawMessage(`{"cwd":"/workspace"}`),
	}, codexrelay.CallPlan{Forward: true}, result, nil)
	require.NoError(t, err)
	require.JSONEq(t, string(result), string(completed))
	require.Less(t, time.Since(started), 50*time.Millisecond,
		"Discord/Control 不可用不得延迟 Desktop thread/start 响应")
}

func TestDesktopToolRuntimeUsesBoundDiscordWorkspace(t *testing.T) {
	environmentID := uuid.New()
	environmentRuntime := devcontainer.Runtime{
		EnvironmentID: environmentID,
		Container:     "desktop-environment",
		User:          "vscode",
		UID:           1000,
		GID:           1000,
		Home:          "/home/vscode",
	}
	task := workerprotocol.Task{Snapshot: workerprotocol.TaskSnapshot{
		Discord: &workerprotocol.DiscordSnapshot{Development: &workerprotocol.DevelopmentSpec{
			EnvironmentID:     environmentID,
			WorkspaceRelative: "workspaces/wakeqora",
		}},
	}}

	runtime, err := desktopRuntimeForTask(environmentRuntime, &task)
	require.NoError(t, err)
	require.Equal(t, "/var/lib/tyrs-hand/workspaces/wakeqora", runtime.Workspace)
	require.Equal(t, environmentRuntime.Container, runtime.Container)
}

func TestDesktopToolRuntimeRejectsMissingDevelopmentSnapshot(t *testing.T) {
	_, err := desktopRuntimeForTask(devcontainer.Runtime{EnvironmentID: uuid.New()},
		&workerprotocol.Task{})
	require.EqualError(t, err, "desktop turn 缺少 Discord 开发环境快照")
}

func TestDesktopEventReporterPersistsUntilControlAcceptsTerminal(t *testing.T) {
	var available atomic.Bool
	var completed atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter,
		request *http.Request,
	) {
		if !available.Load() {
			http.Error(response, "unavailable", http.StatusServiceUnavailable)
			return
		}
		if request.URL.Path == "/worker/v1/runs/run-id-placeholder/events" {
			response.WriteHeader(http.StatusNoContent)
			return
		}
		if request.Method == http.MethodPost {
			completed.Add(1)
			response.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(response, request)
	}))
	t.Cleanup(server.Close)
	root := t.TempDir()
	store, err := newJournalStore(root)
	require.NoError(t, err)
	processor := &RemoteProcessor{cfg: config.Config{ControlTimeout: 100 * time.Millisecond},
		client: workerprotocol.NewClient(server.URL, "node", time.Second), journals: store,
		logger: zap.NewNop()}
	task := &workerprotocol.Task{}
	task.Claimed.RunID = uuid.New()
	task.Claimed.ID = uuid.New()
	task.Claimed.LeaseToken, task.Claimed.LeaseEpoch = "lease", 1
	reporter := newDesktopEventReporter(context.Background(), processor, task)
	reporter.Report("turn/started", json.RawMessage(`{"threadId":"thread"}`))
	loaded, err := store.loadAll()
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	require.Len(t, loaded[0].PendingEvents, 1)

	available.Store(true)
	reporter.Finish(codexcontrol.TurnResult{TurnID: "turn", FinalAnswer: "done"}, nil)
	_, err = os.Stat(store.path(task.Claimed.RunID))
	require.ErrorIs(t, err, os.ErrNotExist)
	require.GreaterOrEqual(t, completed.Load(), int64(1))
}
