//go:build integration && !windows

package sshtransport

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/hostworker"
	"github.com/slovx2/tyrs-hand/internal/officialapp"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestMobileSSHTransportStartsFreshThreadAgainstPinnedCodex(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "tyrs-mobile-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(root)) })
	for _, directory := range []string{"home", "codex-home", "workspaces", "state"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, directory), 0o700))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "codex-home", "config.toml"), []byte(`
model = "mock-model"
approval_policy = "never"
sandbox_mode = "read-only"
model_provider = "mock_provider"

[model_providers.mock_provider]
name = "Mobile transport integration"
base_url = "http://127.0.0.1:1/v1"
wire_api = "responses"
request_max_retries = 0
stream_max_retries = 0
supports_websockets = false
`), 0o600))
	ctx, cancel := context.WithCancel(context.Background())
	runtime, err := hostworker.StartRuntime(ctx, hostworker.RuntimeOptions{
		CodexBin: fixedMobileCodex(t), CodexHome: filepath.Join(root, "codex-home"),
		Home: filepath.Join(root, "home"), WorkspaceRoot: filepath.Join(root, "workspaces"),
		StateDir: filepath.Join(root, "state"), Stdout: io.Discard, Stderr: io.Discard,
		Logger: zap.NewNop(),
	})
	require.NoError(t, err)

	var key keyDescription
	encodedKey, err := GenerateEd25519Key()
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(encodedKey), &key))
	signer, err := parseSigner(key.PrivateKey, "")
	require.NoError(t, err)
	server, err := hostworker.StartSSHServer(ctx, hostworker.SSHOptions{
		ListenAddr: "127.0.0.1:0", HostKeyFile: filepath.Join(root, "host-key"),
		Home: filepath.Join(root, "home"), CodexHome: filepath.Join(root, "codex-home"),
		Shell: "/bin/sh", Runtime: runtime, Logger: zap.NewNop(),
		AuthorizedClients: []hostworker.AuthorizedClient{{
			ID: "mobile-integration", PublicKey: signer.PublicKey(),
		}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		Close("pinned-codex")
		require.NoError(t, runtime.Close())
		require.NoError(t, server.Close())
		cancel()
	})
	host, rawPort, err := net.SplitHostPort(server.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(rawPort)
	require.NoError(t, err)
	fingerprint, err := ProbeHost(host, port, "mobile")
	require.NoError(t, err)

	encodedEndpoint, err := OpenAppServer("pinned-codex", host, port, "mobile",
		key.PrivateKey, "", fingerprint)
	require.NoError(t, err)
	t.Cleanup(func() { Close("pinned-codex") })
	var endpoint appServerEndpoint
	require.NoError(t, json.Unmarshal([]byte(encodedEndpoint), &endpoint))
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()
	// React Native 0.81.5 的 Android WebSocket 基于 OkHttp 4.12.0。这里保留
	// 它与普通 Go WebSocket 不同的 Origin 和 permessage-deflate 握手契约。
	origin := strings.TrimSuffix(strings.Replace(endpoint.URL, "ws://", "http://", 1),
		"/"+endpoint.Token)
	requestHeader := http.Header{
		"Origin":                   {origin},
		"Sec-WebSocket-Extensions": {"permessage-deflate"},
		"User-Agent":               {"okhttp/4.12.0"},
	}
	connection, response, err := websocket.DefaultDialer.DialContext(dialCtx, endpoint.URL,
		requestHeader)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	require.NoError(t, err)
	client, err := codex.ConnectTransport(dialCtx, connection, codex.SocketClientOptions{
		RequestTimeout: 5 * time.Second,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	protocolCtx, protocolCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer protocolCancel()

	var started struct {
		Thread officialapp.Thread `json:"thread"`
	}
	require.NoError(t, client.Call(protocolCtx, "thread/start", map[string]any{
		"cwd": filepath.Join(root, "workspaces"), "model": "mock-model",
		"runtimeWorkspaceRoots": []string{filepath.Join(root, "workspaces")},
	}, &started))
	require.NotEmpty(t, started.Thread.ID)
	events := client.Subscribe(codex.ThreadFilter{ThreadID: started.Thread.ID})
	t.Cleanup(events.Close)

	// 固定 Codex 的官方契约：首次用户消息落盘前，resume 没有 rollout 可读；
	// 客户端必须使用 thread/start 返回的内存 Thread 直接发起 turn/start。
	err = client.Call(protocolCtx, "thread/resume", map[string]any{
		"threadId": started.Thread.ID,
	}, nil)
	require.ErrorContains(t, err, "no rollout found for thread id")

	var turn struct {
		Turn officialapp.Turn `json:"turn"`
	}
	require.NoError(t, client.Call(protocolCtx, "turn/start", map[string]any{
		"threadId": started.Thread.ID, "clientUserMessageId": "mobile-first-message",
		"input": []officialapp.UserInput{officialapp.TextInput("reply only OK")},
		"model": "mock-model",
	}, &turn))
	require.NotEmpty(t, turn.Turn.ID)
	waitForUserMessageItem(t, protocolCtx, events.Events(), "mobile-first-message")

	var read struct {
		Thread officialapp.Thread `json:"thread"`
	}
	require.NoError(t, client.Call(protocolCtx, "thread/read", map[string]any{
		"threadId": started.Thread.ID, "includeTurns": true,
	}, &read))
	require.NotNil(t, read.Thread.FindClientMessage("mobile-first-message"))
}

func waitForUserMessageItem(t *testing.T, ctx context.Context, events <-chan codex.Event,
	clientMessageID string,
) {
	t.Helper()
	for {
		select {
		case event, ok := <-events:
			require.True(t, ok, "官方事件流在首条 userMessage 前关闭")
			if event.Method != "item/started" {
				continue
			}
			var value struct {
				Item officialapp.Item `json:"item"`
			}
			require.NoError(t, json.Unmarshal(event.Params, &value))
			if value.Item.Type == "userMessage" && value.Item.ClientID != nil &&
				*value.Item.ClientID == clientMessageID {
				return
			}
		case <-ctx.Done():
			t.Fatalf("等待官方首条 userMessage 物化超时: %v", ctx.Err())
		}
	}
}

func fixedMobileCodex(t *testing.T) string {
	t.Helper()
	path := os.Getenv("TYRS_HAND_TEST_CODEX_BIN")
	if path == "" {
		path = "codex"
	}
	resolved, err := exec.LookPath(path)
	if err != nil {
		t.Skip("本机没有固定版本 Codex")
	}
	require.NoError(t, codex.ValidateVersion(context.Background(), resolved))
	return resolved
}
