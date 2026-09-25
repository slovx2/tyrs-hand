//go:build integration

package hostworker

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/mobile/sshtransport"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestRuntimeRegistryRealSSHBothEngines(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	bin := os.Getenv("TYRS_HAND_TEST_CODEX_BIN")
	adapter := os.Getenv("TYRS_HAND_TEST_CLAUDE_BIN")
	require.NotEmpty(t, bin, "必需的 Codex CLI 不允许 skip")
	require.NotEmpty(t, adapter, "必需的 Claude 适配器不允许 skip")
	root, err := os.MkdirTemp("/tmp", "dual-runtime-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	var modelCalls atomic.Int64
	var requestsMu sync.Mutex
	modelRequests := map[runtimeidentity.Engine][]json.RawMessage{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/messages" || request.URL.Path == "/v1/responses" {
			modelCalls.Add(1)
			body, err := io.ReadAll(io.LimitReader(request.Body, 4<<20))
			require.NoError(t, err)
			engine := runtimeidentity.Codex
			if request.URL.Path == "/v1/messages" {
				engine = runtimeidentity.Claude
				if strings.Contains(string(body), "hello from phone") {
					require.Equal(t, "Bearer test-token", request.Header.Get("Authorization"))
					require.Empty(t, request.Header.Get("x-api-key"), "切换认证方式后不能继续发送旧密钥")
				} else {
					require.Equal(t, "test-not-a-secret", request.Header.Get("x-api-key"), "认证必须来自独立 settings.json")
				}
			}
			requestsMu.Lock()
			modelRequests[engine] = append(modelRequests[engine], json.RawMessage(body))
			requestsMu.Unlock()
		}
		dualEngineModel(w, request)
	}))
	t.Cleanup(upstream.Close)
	t.Cleanup(func() {
		if directory := os.Getenv("PROTOCOL_ARTIFACT_DIR"); directory != "" {
			requestsMu.Lock()
			defer requestsMu.Unlock()
			for engine, requests := range modelRequests {
				data, err := json.MarshalIndent(map[string]any{"formatVersion": 1,
					"runId": os.Getenv("PROTOCOL_RUN_ID"), "engine": engine, "caseName": t.Name(),
					"kind": "models", "payload": map[string]any{"requests": requests}}, "", "  ")
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(directory, "models-runtime-"+string(engine)+".json"), data, 0o600))
			}
		}
	})
	_, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromKey(private)
	require.NoError(t, err)
	options := make([]RuntimeEntryOptions, 0, 2)
	claudeExecutable := filepath.Join(root, "claude-runtime")
	for _, engine := range []runtimeidentity.Engine{runtimeidentity.Codex, runtimeidentity.Claude} {
		home := filepath.Join(root, string(engine))
		configHome := filepath.Join(home, "config")
		require.NoError(t, os.MkdirAll(configHome, 0o700))
		envFile := filepath.Join(home, "runtime.env")
		command := bin
		if engine == runtimeidentity.Claude {
			command = claudeExecutable
			require.NoError(t, os.WriteFile(envFile, []byte("CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1\n"), 0o600))
			claudeConfig := filepath.Join(configHome, "claude")
			require.NoError(t, os.MkdirAll(claudeConfig, 0o700))
			settings, err := json.Marshal(map[string]any{"model": "claude-config-model", "env": map[string]string{
				"ANTHROPIC_API_KEY": "test-not-a-secret", "ANTHROPIC_BASE_URL": upstream.URL,
			}})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(claudeConfig, "settings.json"), settings, 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(claudeConfig, "CLAUDE.md"), []byte("CLAUDE_RUNTIME_INSTRUCTIONS_7319"), 0o600))
		} else {
			configuration := fmt.Sprintf("model = \"mock-model\"\nmodel_provider = \"mock\"\napproval_policy = \"never\"\n[model_providers.mock]\nname = \"Mock\"\nbase_url = %q\nwire_api = \"responses\"\nrequest_max_retries = 0\nstream_max_retries = 0\nsupports_websockets = false\n", upstream.URL+"/v1")
			require.NoError(t, os.WriteFile(filepath.Join(configHome, "config.toml"), []byte(configuration), 0o600))
		}
		options = append(options, RuntimeEntryOptions{
			Runtime: RuntimeOptions{Engine: engine, WorkerID: "one-worker", CodexBin: command, CodexHome: configHome,
				EntryCommand: []string{os.Args[0], "-test.run=^TestRuntimeEntryHelperProcess$", "--"},
				Home:         home, WorkspaceRoot: filepath.Join(root, "project"), StateDir: filepath.Join(home, "state"), EnvFile: envFile,
				Environment: []string{"PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8"}, CodexStdout: io.Discard, CodexStderr: io.Discard},
			SSH: SSHOptions{ListenAddr: map[runtimeidentity.Engine]string{runtimeidentity.Codex: "127.0.0.1:0", runtimeidentity.Claude: "localhost:0"}[engine],
				HostKeyFile: filepath.Join(home, "host_key"), Home: home, CodexHome: configHome,
				AuthorizedClients: []AuthorizedClient{{ID: "test-client", PublicKey: signer.PublicKey()}}},
		})
	}
	registry, err := StartRuntimeRegistry(ctx, options)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, registry.Close()) })
	// 初次启动缺少 Claude 制品仍保留 Codex 和两个入口，修复制品后独立恢复。
	require.Equal(t, "running", registry.entries[runtimeidentity.Codex].Runtime.Info().Status)
	require.Equal(t, "unavailable", registry.entries[runtimeidentity.Claude].Runtime.Info().Status)
	require.Error(t, registry.Restart(runtimeidentity.Claude))
	launcher := "#!/bin/sh\nexec '" + strings.ReplaceAll(adapter, "'", "'\"'\"'") + "' \"$@\"\n"
	require.NoError(t, os.WriteFile(claudeExecutable, []byte(launcher), 0o700))
	require.NoError(t, registry.Restart(runtimeidentity.Claude))
	clients := map[runtimeidentity.Engine]*ssh.Client{}
	protocol := map[runtimeidentity.Engine]*codex.SocketClient{}
	threads := map[runtimeidentity.Engine]string{}
	for _, engine := range []runtimeidentity.Engine{runtimeidentity.Codex, runtimeidentity.Claude} {
		entry, err := registry.Entry(engine)
		require.NoError(t, err)
		connection, err := ssh.Dial("tcp", entry.SSH.Addr().String(), &ssh.ClientConfig{
			User: "test", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, Timeout: 5 * time.Second,
			HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
				if ssh.FingerprintSHA256(key) != entry.SSH.HostKeyFingerprint() {
					return fmt.Errorf("Host Key 不匹配")
				}
				return nil
			},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = connection.Close() })
		clients[engine] = connection
		for _, command := range []string{"codex --version", "codex app-server daemon start", "codex app-server daemon start", "tyrs-hand-worker runtime info",
			"command -v codex", "exec 'codex' --version", "exec 'codex' app-server daemon start", "exec 'tyrs-hand-worker' runtime info"} {
			session, err := connection.NewSession()
			require.NoError(t, err)
			output, err := session.Output(command)
			require.NoError(t, err)
			if strings.HasSuffix(command, "runtime info") {
				var info RuntimeInfo
				require.NoError(t, json.Unmarshal(output, &info))
				require.Equal(t, engine, info.Engine)
				require.Equal(t, "one-worker", info.WorkerID)
			}
			if command == "command -v codex" {
				require.Equal(t, filepath.Join(entry.Runtime.EntryBin(), "codex"), strings.TrimSpace(string(output)))
			}
			if strings.HasSuffix(command, "--version") {
				require.Equal(t, "codex-cli 0.147.0\n", string(output))
			}
		}
		session, err := connection.NewSession()
		require.NoError(t, err)
		_, err = session.Output("exec 'codex' app-server --listen stdio://")
		require.Error(t, err, "不允许包装器另启 app-server")
		client := connectRuntimeSSH(t, ctx, connection, engine)
		protocol[engine] = client
		var started struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
		}
		require.NoError(t, client.Call(ctx, "thread/start", map[string]any{"cwd": options[0].Runtime.WorkspaceRoot, "approvalPolicy": "never", "sandbox": "danger-full-access"}, &started))
		threads[engine] = started.Thread.ID
	}
	require.NotEqual(t, registry.entries[runtimeidentity.Codex].SSH.HostKeyFingerprint(), registry.entries[runtimeidentity.Claude].SSH.HostKeyFingerprint())
	for _, engine := range []runtimeidentity.Engine{runtimeidentity.Codex, runtimeidentity.Claude} {
		client := protocol[engine]
		subscription := client.Subscribe(codex.ThreadFilter{ThreadID: threads[engine]})
		t.Cleanup(subscription.Close)
		other := runtimeidentity.Codex
		if engine == runtimeidentity.Codex {
			other = runtimeidentity.Claude
		}
		var ignored any
		require.Error(t, client.Call(ctx, "thread/read", map[string]any{"threadId": threads[other]}, &ignored))
		var started struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		require.NoError(t, client.Call(ctx, "turn/start", map[string]any{"threadId": threads[engine], "clientUserMessageId": "same-message-id", "input": []map[string]any{{"type": "text", "text": "hello", "text_elements": []any{}}}}, &started))
		completed := false
		for !completed {
			select {
			case <-ctx.Done():
				t.Fatal("真实模型链路超时")
			case event := <-subscription.Events():
				if event.Method == "turn/completed" {
					var result struct {
						Turn struct {
							ID, Status string
							Error      any
						} `json:"turn"`
					}
					require.NoError(t, json.Unmarshal(event.Params, &result))
					require.Equal(t, started.Turn.ID, result.Turn.ID)
					require.Equal(t, "completed", result.Turn.Status, "%v", result.Turn.Error)
					completed = true
				}
			}
		}
	}
	// 手机使用正式 Go SSH Transport，再接入已由桌面创建的同一 Claude Thread。
	// 模拟控制台更新后的原生文件，下一轮 SDK 必须读取新认证，不能使用缓存密钥。
	updatedSettings, err := json.Marshal(map[string]any{"model": "claude-config-model", "env": map[string]string{
		"ANTHROPIC_BASE_URL": upstream.URL, "ANTHROPIC_AUTH_TOKEN": "test-token",
	}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(options[1].Runtime.CodexHome, "claude", "settings.json"), updatedSettings, 0o600))
	privateBlock, err := ssh.MarshalPrivateKey(private, "protocol-test")
	require.NoError(t, err)
	entry := registry.entries[runtimeidentity.Claude]
	address := entry.SSH.Addr().(*net.TCPAddr)
	phoneEndpoint, err := sshtransport.OpenAppServer("claude-phone", address.IP.String(), address.Port,
		"mobile", string(pem.EncodeToMemory(privateBlock)), "", entry.SSH.HostKeyFingerprint())
	require.NoError(t, err)
	t.Cleanup(func() { sshtransport.Close("claude-phone") })
	var endpoint struct {
		URL     string      `json:"url"`
		Runtime RuntimeInfo `json:"runtime"`
	}
	require.NoError(t, json.Unmarshal([]byte(phoneEndpoint), &endpoint))
	require.Equal(t, runtimeidentity.Claude, endpoint.Runtime.Engine)
	require.Equal(t, "one-worker", endpoint.Runtime.WorkerID)
	phoneWS, _, err := websocket.DefaultDialer.DialContext(ctx, endpoint.URL, nil)
	require.NoError(t, err)
	phoneTrace := &protocolTraceTransport{MessageTransport: phoneWS}
	t.Cleanup(func() { phoneTrace.save(t, runtimeidentity.Claude) })
	phone, err := codex.ConnectTransport(ctx, phoneTrace, codex.SocketClientOptions{RequestTimeout: 10 * time.Second})
	require.NoError(t, err)
	t.Cleanup(func() { _ = phone.Close() })
	phoneEvents := phone.Subscribe(codex.ThreadFilter{ThreadID: threads[runtimeidentity.Claude]})
	t.Cleanup(phoneEvents.Close)
	var phoneResult any
	require.NoError(t, phone.Call(ctx, "thread/resume", map[string]any{
		"threadId": threads[runtimeidentity.Claude], "historyMode": "paginated",
	}, &phoneResult))
	require.NoError(t, phone.Call(ctx, "turn/start", map[string]any{
		"threadId": threads[runtimeidentity.Claude], "clientUserMessageId": "phone-message",
		"input": []map[string]any{{"type": "text", "text": "hello from phone", "text_elements": []any{}}},
	}, &phoneResult))
	for finished := false; !finished; {
		select {
		case <-ctx.Done():
			t.Fatal("手机追加 Turn 超时")
		case event := <-phoneEvents.Events():
			if event.Method == "turn/completed" {
				var result struct {
					Turn struct {
						Status string `json:"status"`
					} `json:"turn"`
				}
				require.NoError(t, json.Unmarshal(event.Params, &result))
				require.Equal(t, "completed", result.Turn.Status)
				finished = true
			}
		}
	}
	var handedBack struct {
		Thread struct {
			Turns []json.RawMessage `json:"turns"`
		} `json:"thread"`
	}
	require.NoError(t, protocol[runtimeidentity.Claude].Call(ctx, "thread/read",
		map[string]any{"threadId": threads[runtimeidentity.Claude], "includeTurns": true}, &handedBack))
	require.Len(t, handedBack.Thread.Turns, 2, "桌面读取同一 Claude 会话时应看到手机追加的 Turn")
	requestsMu.Lock()
	claudeRequests := append([]json.RawMessage(nil), modelRequests[runtimeidentity.Claude]...)
	requestsMu.Unlock()
	require.Len(t, claudeRequests, 2)
	require.Contains(t, string(claudeRequests[0]), "CLAUDE_RUNTIME_INSTRUCTIONS_7319", "原生 CLAUDE.md 必须进入模型上下文")
	var configuredRequest struct {
		Model string `json:"model"`
	}
	require.NoError(t, json.Unmarshal(claudeRequests[0], &configuredRequest))
	require.Equal(t, "claude-config-model", configuredRequest.Model, "客户端未指定模型时必须读取 settings.json 的 model")
	require.Contains(t, string(claudeRequests[1]), "hello from phone")
	require.Contains(t, string(claudeRequests[1]), "CLAUDE_OK", "手机追加必须恢复桌面首轮的原生上下文")
	require.NotContains(t, string(claudeRequests[1]), "CODEX_OK", "另一引擎的历史不得进入 Claude 上下文")
	before := registry.entries[runtimeidentity.Codex].Runtime.Generation()
	require.NoError(t, registry.Restart(runtimeidentity.Claude))
	require.Equal(t, before, registry.entries[runtimeidentity.Codex].Runtime.Generation())
	var result any
	require.NoError(t, protocol[runtimeidentity.Codex].Call(ctx, "thread/read", map[string]any{"threadId": threads[runtimeidentity.Codex]}, &result))
	claudeRuntime := registry.entries[runtimeidentity.Claude].Runtime
	generation := claudeRuntime.Generation()
	claudeRuntime.mu.Lock()
	process := claudeRuntime.current.command.Process
	claudeRuntime.mu.Unlock()
	require.NoError(t, process.Kill())
	require.Eventually(t, func() bool {
		return claudeRuntime.Generation() != generation && claudeRuntime.Info().Status == "running"
	}, 15*time.Second, 50*time.Millisecond)
	require.Equal(t, before, registry.entries[runtimeidentity.Codex].Runtime.Generation())
	require.NoError(t, protocol[runtimeidentity.Codex].Call(ctx, "thread/read", map[string]any{"threadId": threads[runtimeidentity.Codex]}, &result))
	require.Equal(t, int64(3), modelCalls.Load(), "启动、重启和崩溃恢复不能重放模型请求")
	registry.Authorization.Replace(nil)
	for _, connection := range clients {
		require.Eventually(t, func() bool {
			session, err := connection.NewSession()
			if err == nil {
				_ = session.Close()
			}
			return err != nil
		}, time.Second, 10*time.Millisecond)
	}
}

func connectRuntimeSSH(t *testing.T, ctx context.Context, connection *ssh.Client, engine runtimeidentity.Engine) *codex.SocketClient {
	t.Helper()
	channel, requests, err := connection.OpenChannel("session", nil)
	require.NoError(t, err)
	go ssh.DiscardRequests(requests)
	// 引号使请求实际经过 shell 和入口包装器，不命中直接 proxy 快捷路径。
	ok, err := channel.SendRequest("exec", true, ssh.Marshal(struct{ Command string }{"exec 'codex' app-server proxy"}))
	require.NoError(t, err)
	require.True(t, ok)
	dialer := websocket.Dialer{NetDialContext: func(context.Context, string, string) (net.Conn, error) { return testSSHConnection{channel}, nil }}
	ws, response, err := dialer.DialContext(ctx, "ws://worker/", nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	require.NoError(t, err)
	trace := &protocolTraceTransport{MessageTransport: ws}
	t.Cleanup(func() { trace.save(t, engine) })
	client, err := codex.ConnectTransport(ctx, trace, codex.SocketClientOptions{RequestTimeout: 10 * time.Second})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestRuntimeEntryHelperProcess(t *testing.T) {
	for index, arg := range os.Args {
		if arg != "--" || len(os.Args) <= index+2 || os.Args[index+1] != "runtime-entry" {
			continue
		}
		if err := RunEntryCommand(context.Background(), os.Args[index+2], os.Args[index+3:], os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
}

func dualEngineModel(w http.ResponseWriter, request *http.Request) {
	if request.URL.Path == "/api/hello" {
		_, _ = io.WriteString(w, "{}")
		return
	}
	if strings.Contains(request.URL.Path, "count_tokens") {
		_, _ = io.WriteString(w, `{"input_tokens":10}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	event := func(kind string, data map[string]any) {
		data["type"] = kind
		body, _ := json.Marshal(data)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, body)
	}
	if request.URL.Path == "/v1/messages" {
		event("message_start", map[string]any{"message": map[string]any{"id": "msg_test", "type": "message", "role": "assistant", "model": "claude-sonnet-4-6", "content": []any{}, "stop_reason": nil, "usage": map[string]int{"input_tokens": 10, "output_tokens": 1}}})
		event("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
		event("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "text_delta", "text": "CLAUDE_OK"}})
		event("content_block_stop", map[string]any{"index": 0})
		event("message_delta", map[string]any{"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil}, "usage": map[string]int{"output_tokens": 5}})
		event("message_stop", map[string]any{})
		return
	}
	if request.URL.Path != "/v1/responses" {
		w.WriteHeader(404)
		return
	}
	event("response.created", map[string]any{"response": map[string]any{"id": "resp-test"}})
	event("response.output_item.done", map[string]any{"item": map[string]any{"id": "msg-test", "type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "CODEX_OK"}}}})
	event("response.completed", map[string]any{"response": map[string]any{"id": "resp-test", "usage": map[string]any{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15, "input_tokens_details": nil, "output_tokens_details": nil}}})
}
