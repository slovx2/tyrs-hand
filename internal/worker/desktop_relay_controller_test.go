package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/codexrelay"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/devcontainer"
	"github.com/slovx2/tyrs-hand/internal/hostworker"
	"github.com/slovx2/tyrs-hand/internal/participantidentity"
	"github.com/slovx2/tyrs-hand/internal/settings"
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

func TestDesktopImagesFromTurnValidatesAndDeduplicatesLocalImages(t *testing.T) {
	root := t.TempDir()
	validPath := filepath.Join(root, "shot.png")
	content := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
	require.NoError(t, os.WriteFile(validPath, content, 0o600))
	wrongExtension := filepath.Join(root, "shot.jpg")
	require.NoError(t, os.WriteFile(wrongExtension, content, 0o600))
	symlinkPath := filepath.Join(root, "link.png")
	require.NoError(t, os.Symlink(validPath, symlinkPath))
	params, err := json.Marshal(map[string]any{"input": []map[string]string{
		{"type": "localImage", "path": validPath},
		{"type": "localImage", "path": validPath},
		{"type": "localImage", "path": wrongExtension},
		{"type": "localImage", "path": symlinkPath},
		{"type": "localImage", "path": filepath.Join(root, "missing.png")},
	}})
	require.NoError(t, err)

	images, notice, err := desktopImagesFromTurn(context.Background(), params,
		openLocalDesktopImage)

	require.NoError(t, err)
	require.Empty(t, notice)
	require.Len(t, images, 4)
	require.Equal(t, validPath, images[0].SourcePath)
	require.Equal(t, "image/png", images[0].MediaType)
	require.Len(t, images[0].SHA256, 64)
	require.Contains(t, images[1].Error, "不匹配")
	require.NotEmpty(t, images[2].Error)
	require.NotEmpty(t, images[3].Error)
	encoded, err := json.Marshal(images[0])
	require.NoError(t, err)
	require.NotContains(t, string(encoded), validPath)
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

func TestDesktopRelayConfiguresManagedRuntimeAcrossThreadLifecycle(t *testing.T) {
	environmentID := uuid.New()
	requestID := uuid.New()
	control := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter,
		request *http.Request,
	) {
		unwrapWorkerTestRequest(t, request)
		require.Equal(t, "/worker/v1/desktop-thread-requests", request.URL.Path)
		require.NoError(t, json.NewEncoder(response).Encode(workerprotocol.DesktopThreadState{
			ID: requestID, EnvironmentID: environmentID, Operation: "start", Status: "preparing",
		}))
	}))
	t.Cleanup(control.Close)
	controller := &desktopRelayController{
		processor: &RemoteProcessor{cfg: config.Config{
			ControlTimeout: time.Second,
			BrowserMCPURL:  "http://host.docker.internal:8931/mcp",
		},
			client: workerprotocol.NewClient(control.URL, "credential", time.Second),
			logger: zap.NewNop()},
		environment: &environmentCodex{runtime: devcontainer.Runtime{
			EnvironmentID: environmentID, ModelSource: "provider",
			ModelBaseURL: "https://api.example.com/v1",
		}},
	}
	plan, err := controller.PrepareCall(context.Background(), codexrelay.Call{
		Role: codexrelay.RoleDesktop, Method: "thread/start",
		Params: json.RawMessage(`{
			"cwd":"/workspace",
			"effort":"xhigh",
			"serviceTier":"fast",
			"dynamicTools":[{"type":"namespace","name":"personal","tools":[]}],
			"config":{
				"model_providers":{"personal":{"base_url":"https://personal.example/v1"}},
				"mcp_servers":{
					"personal":{"url":"https://mcp.example"},
					"chrome":{"url":"https://user.example/mcp"}
				},
				"shell_environment_policy":{
					"set":{"PERSONAL":"keep","TYRS_HAND_MODEL_API_KEY":"leak","TYRS_BROWSER_MCP_TOKEN":"leak"},
					"exclude":["PERSONAL_SECRET"]
				}
			}
		}`),
	})
	require.NoError(t, err)
	var params map[string]any
	require.NoError(t, json.Unmarshal(plan.Params, &params))
	runtimeConfig := params["config"].(map[string]any)
	require.Equal(t, "tyrs-hand-provider", runtimeConfig["model_provider"])
	require.Equal(t, "xhigh", runtimeConfig["model_reasoning_effort"])
	require.Equal(t, "fast", runtimeConfig["service_tier"])
	require.NotContains(t, params, "effort")
	require.NotContains(t, params, "serviceTier")
	require.Contains(t, runtimeConfig["model_providers"], "personal")
	mcpServers := runtimeConfig["mcp_servers"].(map[string]any)
	require.Contains(t, mcpServers, "personal")
	chrome := mcpServers["chrome"].(map[string]any)
	require.Equal(t, "http://host.docker.internal:8931/mcp", chrome["url"])
	require.Equal(t, false, chrome["required"])
	policy := runtimeConfig["shell_environment_policy"].(map[string]any)
	require.NotContains(t, policy["set"], "TYRS_HAND_MODEL_API_KEY")
	require.NotContains(t, policy["set"], "TYRS_BROWSER_MCP_TOKEN")
	require.ElementsMatch(t, []any{"PERSONAL_SECRET", "TYRS_HAND_MODEL_API_KEY",
		"TYRS_BROWSER_MCP_TOKEN"},
		policy["exclude"])
	tools := params["dynamicTools"].([]any)
	encodedTools, err := json.Marshal(tools)
	require.NoError(t, err)
	require.Contains(t, string(encodedTools), `"name":"personal"`)
	require.Contains(t, string(encodedTools), `"name":"git"`)
	require.NotContains(t, string(encodedTools), `"name":"github"`)

	fork, err := controller.PrepareCall(context.Background(), codexrelay.Call{
		Role: codexrelay.RoleDesktop, Method: "thread/fork",
		Params: json.RawMessage(`{"threadId":"thread","config":{
			"model_providers":{"personal":{"base_url":"https://personal.example/v1"}},
			"mcp_servers":{"personal":{"url":"https://mcp.example"}}
		}}`),
	})
	require.NoError(t, err)
	var forkParams map[string]any
	require.NoError(t, json.Unmarshal(fork.Params, &forkParams))
	forkConfig := forkParams["config"].(map[string]any)
	require.Equal(t, "tyrs-hand-provider", forkConfig["model_provider"])
	require.Contains(t, forkConfig["model_providers"], "personal")
	require.Contains(t, forkConfig["mcp_servers"], "personal")
	require.Contains(t, forkConfig["mcp_servers"], "chrome")
	require.Contains(t, forkParams["developerInstructions"], participantidentity.IdentityContextKey)
	require.NotContains(t, forkParams, "dynamicTools")

	controller.environment.runtime.ModelSource = "chatgpt"
	resume, err := controller.PrepareCall(context.Background(), codexrelay.Call{
		Role: codexrelay.RoleDesktop, Method: "thread/resume",
		Params: json.RawMessage(`{"threadId":"thread","config":{}}`),
	})
	require.NoError(t, err)
	var resumeParams map[string]any
	require.NoError(t, json.Unmarshal(resume.Params, &resumeParams))
	resumeConfig := resumeParams["config"].(map[string]any)
	require.Equal(t, "openai", resumeConfig["model_provider"])
	resumeMCP := resumeConfig["mcp_servers"].(map[string]any)
	require.Contains(t, resumeMCP, "chrome")
	require.NotContains(t, string(resume.Params), `"dynamicTools"`)
	require.Contains(t, resumeParams["developerInstructions"], participantidentity.IdentityContextKey)
}

func TestDesktopRelayInjectsRuntimeIntoEphemeralThreadWithoutTools(t *testing.T) {
	controller := &desktopRelayController{
		processor: &RemoteProcessor{cfg: config.Config{
			BrowserMCPURL: "http://host.docker.internal:8931/mcp",
		}},
		environment: &environmentCodex{runtime: devcontainer.Runtime{
			ModelSource:  settings.ModelSourceProvider,
			ModelBaseURL: "https://api.example.com/v1",
		}},
	}
	for _, method := range []string{"thread/start", "thread/fork"} {
		t.Run(method, func(t *testing.T) {
			configured, err := controller.ConfigureEphemeralThread(context.Background(),
				codexrelay.Call{Role: codexrelay.RoleDesktop, Method: method,
					Params: json.RawMessage(`{
						"threadId":"source-thread",
						"ephemeral":true,
						"model":"gpt-5.6-luna",
						"dynamicTools":null,
						"config":{"features.plugins":false,"web_search":"disabled"}
					}`),
				})
			require.NoError(t, err)
			var params map[string]any
			require.NoError(t, json.Unmarshal(configured, &params))
			runtimeConfig := params["config"].(map[string]any)
			require.Equal(t, "tyrs-hand-provider", runtimeConfig["model_provider"])
			require.Equal(t, false, runtimeConfig["features.plugins"])
			require.Equal(t, "disabled", runtimeConfig["web_search"])
			provider := runtimeConfig["model_providers"].(map[string]any)["tyrs-hand-provider"].(map[string]any)
			require.Equal(t, "https://api.example.com/v1", provider["base_url"])
			require.Nil(t, params["dynamicTools"])
			require.NotContains(t, runtimeConfig, "mcp_servers")
		})
	}
}

func TestDesktopRelayKeepsMachineProviderForHostRuntime(t *testing.T) {
	controller := &desktopRelayController{environment: &environmentCodex{
		hostRuntime: &hostworker.Runtime{},
	}}
	configured := controller.injectDesktopRuntime(json.RawMessage(`{
		"serviceTier":"priority",
		"config":{
			"model_provider":"machine-provider",
			"model_providers":{
				"machine-provider":{"requires_openai_auth":true}
			}
		}
	}`), desktopRuntimeInjection{})
	var params map[string]any
	require.NoError(t, json.Unmarshal(configured, &params))
	runtimeConfig := params["config"].(map[string]any)
	require.Equal(t, "machine-provider", runtimeConfig["model_provider"])
	require.Equal(t, "priority", runtimeConfig["service_tier"])
	require.Contains(t, runtimeConfig["model_providers"], "machine-provider")
	require.NotContains(t, runtimeConfig["model_providers"], "tyrs-hand-provider")
}

func TestDesktopRelayAlwaysListsEveryProvider(t *testing.T) {
	controller := &desktopRelayController{environment: &environmentCodex{}}
	for _, test := range []struct {
		name     string
		params   string
		expected string
	}{
		{
			name: "省略 Provider", params: `{"archived":false}`,
			expected: `{"archived":false,"modelProviders":[]}`,
		},
		{
			name: "Provider 为 null", params: `{"modelProviders":null,"limit":50}`,
			expected: `{"modelProviders":[],"limit":50}`,
		},
		{
			name: "覆盖桌面端 OpenAI 过滤", params: `{"modelProviders":["openai"],"limit":50}`,
			expected: `{"modelProviders":[],"limit":50}`,
		},
		{
			name:     "覆盖平台 Provider 过滤",
			params:   `{"modelProviders":["tyrs-hand-provider"],"limit":50}`,
			expected: `{"modelProviders":[],"limit":50}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, err := controller.PrepareCall(context.Background(), codexrelay.Call{
				Role: codexrelay.RoleDesktop, Method: "thread/list",
				Params: json.RawMessage(test.params),
			})
			require.NoError(t, err)
			require.True(t, plan.Forward)
			require.JSONEq(t, test.expected, string(plan.Params))
		})
	}
}

func TestDesktopRelayAlwaysForwardsCodexModelCatalog(t *testing.T) {
	controller := &desktopRelayController{environment: &environmentCodex{
		runtime: devcontainer.Runtime{ModelSource: settings.ModelSourceChatGPT},
	}}
	chatGPTPlan, err := controller.PrepareCall(context.Background(), codexrelay.Call{
		Role: codexrelay.RoleDesktop, Method: "model/list", Params: json.RawMessage(`{}`),
	})
	require.NoError(t, err)
	require.True(t, chatGPTPlan.Forward)

	controller.environment.runtime.ModelSource = settings.ModelSourceProvider
	providerPlan, err := controller.PrepareCall(context.Background(), codexrelay.Call{
		Role: codexrelay.RoleDesktop, Method: "model/list", Params: json.RawMessage(`{}`),
	})
	require.NoError(t, err)
	require.True(t, providerPlan.Forward)

	workerPlan, err := controller.PrepareCall(context.Background(), codexrelay.Call{
		Role: codexrelay.RoleWorker, Method: "model/list", Params: json.RawMessage(`{}`),
	})
	require.NoError(t, err)
	require.True(t, workerPlan.Forward)
}

func TestDesktopRelayAccountCapabilitiesFollowModelSource(t *testing.T) {
	chatGPTResult := json.RawMessage(
		`{"account":{"type":"chatgpt","email":"user@example.com","planType":"free"},` +
			`"requiresOpenaiAuth":true}`)
	desktopResult, err := desktopAccountForModelSource(
		settings.ModelSourceProvider, chatGPTResult, nil,
	)
	require.NoError(t, err)
	require.JSONEq(t, `{"account":{"type":"chatgpt","email":null,"planType":"unknown"},`+
		`"requiresOpenaiAuth":false}`, string(desktopResult))

	preserved, err := desktopAccountForModelSource(
		settings.ModelSourceChatGPT, chatGPTResult, nil,
	)
	require.NoError(t, err)
	require.JSONEq(t, string(chatGPTResult), string(preserved))
}

func TestDesktopRelayKeepsMachineAccountCapabilitiesForHostRuntime(t *testing.T) {
	chatGPTResult := json.RawMessage(
		`{"account":{"type":"chatgpt","email":"user@example.com","planType":"free"},` +
			`"requiresOpenaiAuth":true}`)
	controller := &desktopRelayController{environment: &environmentCodex{
		runtime:     devcontainer.Runtime{ModelSource: settings.ModelSourceProvider},
		hostRuntime: &hostworker.Runtime{},
	}}
	result, err := controller.CompleteCall(context.Background(), codexrelay.Call{
		Role: codexrelay.RoleDesktop, Method: "account/read",
	}, codexrelay.CallPlan{}, chatGPTResult, nil)
	require.NoError(t, err)
	require.JSONEq(t, string(chatGPTResult), string(result))
}

func TestDesktopThreadCompletionDoesNotWaitForDiscordControl(t *testing.T) {
	requestID := uuid.New()
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
	}, codexrelay.CallPlan{Forward: true, State: &desktopThreadCallState{
		request: workerprotocol.DesktopThreadState{
			ID: requestID, EnvironmentID: controller.environment.runtime.EnvironmentID,
			Status: "preparing",
		},
	}}, result, nil)
	require.NoError(t, err)
	require.JSONEq(t, string(result), string(completed))
	require.Less(t, time.Since(started), 50*time.Millisecond,
		"Discord/Control 不可用不得延迟 Desktop thread/start 响应")
}

func TestDesktopRelayRejectsInvalidCWDBeforeCallingAppServer(t *testing.T) {
	var prepareCalls atomic.Int64
	control := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter,
		request *http.Request,
	) {
		prepareCalls.Add(1)
		http.Error(response, "cwd 没有匹配本环境的 Development Forum", http.StatusForbidden)
	}))
	t.Cleanup(control.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	processor := &RemoteProcessor{cfg: config.Config{ControlTimeout: time.Second},
		client: workerprotocol.NewClient(control.URL, "credential", time.Second), logger: zap.NewNop()}
	processor.environments = &environmentCodexRegistry{ctx: ctx, processor: processor,
		entries: make(map[uuid.UUID]*environmentCodex)}
	controller := &desktopRelayController{processor: processor, environment: &environmentCodex{
		runtime: devcontainer.Runtime{EnvironmentID: uuid.New()},
	}}
	mock, err := mockcodex.Start(t)
	require.NoError(t, err)
	relayRoot, err := os.MkdirTemp("/tmp", "tyrs-invalid-cwd-relay-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(relayRoot) })
	relay, err := codexrelay.Start(ctx, codexrelay.Options{
		SocketPath: relayRoot + "/relay.sock", UpstreamSocketPath: mock.SocketPath,
		Controller: controller,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = relay.Close() })
	client, err := relay.OpenClient(codexrelay.ClientOptions{Role: codexrelay.RoleDesktop})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	_, err = client.StartThread(context.Background(),
		json.RawMessage(`{"cwd":"/var/lib/tyrs-hand/workspaces/missing"}`))
	require.ErrorContains(t, err, "403")
	require.Equal(t, int64(1), prepareCalls.Load())
	require.Zero(t, mock.RequestCount("thread/start"),
		"cwd 预检失败后不得调用 app-server 创建 Thread")
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, int64(1), prepareCalls.Load(), "永久性 403 不得后台重试")
}

func TestDesktopRelayPreparesStartAndForkBeforeCompleting(t *testing.T) {
	environmentID := uuid.New()
	requestID := uuid.New()
	operations := make(chan string, 4)
	completed := make(chan struct{}, 1)
	failed := make(chan struct{}, 1)
	control := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter,
		request *http.Request,
	) {
		unwrapWorkerTestRequest(t, request)
		switch {
		case request.URL.Path == "/worker/v1/desktop-thread-requests":
			var input workerprotocol.DesktopThreadPrepareRequest
			require.NoError(t, json.NewDecoder(request.Body).Decode(&input))
			operations <- input.Operation
			require.Len(t, input.RequestKey, 64)
			require.NoError(t, json.NewEncoder(response).Encode(workerprotocol.DesktopThreadState{
				ID: requestID, EnvironmentID: environmentID,
				Operation: input.Operation, Status: "preparing",
			}))
		case strings.HasSuffix(request.URL.Path, "/complete"):
			completed <- struct{}{}
			require.NoError(t, json.NewEncoder(response).Encode(workerprotocol.DesktopThreadState{
				ID: requestID, EnvironmentID: environmentID, Status: "waiting_for_input",
			}))
		case strings.HasSuffix(request.URL.Path, "/fail"):
			failed <- struct{}{}
			response.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(control.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	processor := &RemoteProcessor{cfg: config.Config{ControlTimeout: time.Second},
		client: workerprotocol.NewClient(control.URL, "credential", time.Second), logger: zap.NewNop()}
	processor.environments = &environmentCodexRegistry{ctx: ctx, processor: processor,
		entries: make(map[uuid.UUID]*environmentCodex)}
	controller := &desktopRelayController{processor: processor, environment: &environmentCodex{
		runtime: devcontainer.Runtime{EnvironmentID: environmentID},
	}}

	startPlan, err := controller.PrepareCall(ctx, codexrelay.Call{
		Role: codexrelay.RoleDesktop, Method: "thread/start",
		Params: json.RawMessage(`{"cwd":"/var/lib/tyrs-hand/workspaces/atlas"}`),
	})
	require.NoError(t, err)
	require.Equal(t, "start", <-operations)
	_, err = controller.CompleteCall(ctx, codexrelay.Call{
		Role: codexrelay.RoleDesktop, Method: "thread/start",
	}, startPlan, json.RawMessage(`{"thread":{"id":"desktop-thread"}}`), nil)
	require.NoError(t, err)
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("Desktop Thread 成功后没有提交 reservation")
	}

	forkPlan, err := controller.PrepareCall(ctx, codexrelay.Call{
		Role: codexrelay.RoleDesktop, Method: "thread/fork",
		Params: json.RawMessage(`{"threadId":"desktop-thread"}`),
	})
	require.NoError(t, err)
	require.Equal(t, "fork", <-operations)
	upstreamErr := errors.New("app-server fork 失败")
	_, err = controller.CompleteCall(ctx, codexrelay.Call{
		Role: codexrelay.RoleDesktop, Method: "thread/fork",
	}, forkPlan, nil, upstreamErr)
	require.ErrorIs(t, err, upstreamErr)
	select {
	case <-failed:
	case <-time.After(time.Second):
		t.Fatal("app-server 失败后没有释放 Desktop Thread reservation")
	}
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
		Development: &workerprotocol.DevelopmentSnapshot{Development: &workerprotocol.DevelopmentSpec{
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
	require.EqualError(t, err, "desktop turn 缺少开发环境快照")
}

func TestHostWorkspacePathRejectsEscape(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspaces")
	path, err := hostWorkspacePath(root, "project")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root, "project"), path)

	_, err = hostWorkspacePath(root, "../outside")
	require.Error(t, err)
	_, err = hostWorkspacePath(root, "/outside")
	require.Error(t, err)
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
