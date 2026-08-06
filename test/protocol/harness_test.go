//go:build integration

package protocol

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/appserverhub"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/stretchr/testify/require"
)

func fixedCodexBinary(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("TYRS_HAND_TEST_CODEX_BIN")
	if bin == "" {
		bin = "codex"
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		if os.Getenv("CI") == "true" {
			t.Fatalf("CI 缺少固定 Codex: %v", err)
		}
		t.Skip("本机没有安装满足最低版本要求的 Codex")
	}
	require.NoError(t, codex.ValidateVersion(context.Background(), path))
	return path
}

func temporaryDir(t *testing.T, prefix string) string {
	t.Helper()
	root, err := os.MkdirTemp("", prefix)
	require.NoError(t, err)
	t.Cleanup(func() {
		var removeErr error
		for attempt := 0; attempt < 10; attempt++ {
			removeErr = os.RemoveAll(root)
			if removeErr == nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		require.NoError(t, removeErr)
	})
	return root
}

func sse(events ...map[string]any) string {
	var result strings.Builder
	for _, event := range events {
		data, _ := json.Marshal(event)
		_, _ = fmt.Fprintf(&result, "event: %s\ndata: %s\n\n", event["type"], data)
	}
	return result.String()
}

func completedResponse(id string) map[string]any {
	return map[string]any{
		"type": "response.completed",
		"response": map[string]any{"id": id, "usage": map[string]any{
			"input_tokens": 0, "input_tokens_details": nil, "output_tokens": 0,
			"output_tokens_details": nil, "total_tokens": 0,
		}},
	}
}

func startRealHub(t *testing.T, upstreamURL string,
	controller appserverhub.Controller,
) (*appserverhub.Hub, string) {
	t.Helper()
	root := temporaryDir(t, "tyrs-protocol-hub-")
	home, workspace := filepath.Join(root, "home"), filepath.Join(root, "workspace")
	require.NoError(t, os.MkdirAll(home, 0o700))
	require.NoError(t, os.MkdirAll(workspace, 0o755))
	config := fmt.Sprintf(`model = "mock-model"
approval_policy = "never"
sandbox_mode = "read-only"
model_provider = "mock_provider"

[features]
default_mode_request_user_input = true

[model_providers.mock_provider]
name = "Protocol test mock"
base_url = %q
wire_api = "responses"
request_max_retries = 0
stream_max_retries = 0
supports_websockets = false
`, upstreamURL+"/v1")
	require.NoError(t, os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0o600))
	appSocket := filepath.Join(root, "app.sock")
	process := exec.Command(fixedCodexBinary(t), "app-server", "--listen", "unix://"+appSocket)
	process.Dir = workspace
	process.Env = append(os.Environ(), "CODEX_HOME="+home, "HOME="+root, "RUST_LOG=warn")
	require.NoError(t, process.Start())
	t.Cleanup(func() { _ = process.Process.Kill(); _ = process.Wait() })
	waitForUnixSocket(t, appSocket)
	if controller == nil {
		controller = appserverhub.PassThroughController{}
	}
	hub, err := appserverhub.Start(context.Background(), appserverhub.Options{
		SocketPath: filepath.Join(root, "hub.sock"), UpstreamSocketPath: appSocket,
		Controller: controller,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, hub.Close()) })
	return hub, workspace
}

func waitForUnixSocket(t *testing.T, path string) {
	t.Helper()
	require.Eventually(t, func() bool {
		info, err := os.Stat(path)
		return err == nil && info.Mode()&os.ModeSocket != 0
	}, 10*time.Second, 20*time.Millisecond)
}

func waitForHubTurnCompleted(t *testing.T, events <-chan codex.Event, threadID, turnID string) {
	t.Helper()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event, ok := <-events:
			require.True(t, ok, "App Server 在 Turn 完成前退出")
			if event.Method != "turn/completed" {
				continue
			}
			var params struct {
				ThreadID string `json:"threadId"`
				Turn     struct {
					ID string `json:"id"`
				} `json:"turn"`
			}
			require.NoError(t, json.Unmarshal(event.Params, &params))
			if params.ThreadID == threadID && params.Turn.ID == turnID {
				return
			}
		case <-timer.C:
			t.Fatal("等待真实 Codex Turn 完成超时")
		}
	}
}

func waitForHubTurnStatus(t *testing.T, events <-chan codex.Event, threadID,
	turnID string,
) string {
	t.Helper()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event, ok := <-events:
			require.True(t, ok, "App Server 在 Turn 完成前退出")
			if event.Method != "turn/completed" {
				continue
			}
			var params struct {
				ThreadID string `json:"threadId"`
				Turn     struct {
					ID     string `json:"id"`
					Status string `json:"status"`
				} `json:"turn"`
			}
			require.NoError(t, json.Unmarshal(event.Params, &params))
			if params.ThreadID == threadID && params.Turn.ID == turnID {
				return params.Turn.Status
			}
		case <-timer.C:
			t.Fatal("等待真实 Codex Turn 终态超时")
		}
	}
}

func assertNoTurnEvent(t *testing.T, events <-chan codex.Event, threadID, turnID string,
	duration time.Duration,
) {
	t.Helper()
	timer := time.NewTimer(duration)
	defer timer.Stop()
	for {
		select {
		case event := <-events:
			eventThreadID, eventTurnID := protocolEventScope(event.Params)
			if eventThreadID == threadID && eventTurnID == turnID {
				t.Fatalf("未订阅客户端不应收到 %s: %s", event.Method, event.Params)
			}
		case <-timer.C:
			return
		}
	}
}

func assertNoThreadScopedEvent(t *testing.T, events <-chan codex.Event, threadID string,
	duration time.Duration,
) {
	t.Helper()
	timer := time.NewTimer(duration)
	defer timer.Stop()
	for {
		select {
		case event := <-events:
			eventThreadID, _ := protocolEventScope(event.Params)
			if eventThreadID == "" {
				var value struct {
					Thread struct {
						ID string `json:"id"`
					} `json:"thread"`
				}
				_ = json.Unmarshal(event.Params, &value)
				eventThreadID = value.Thread.ID
			}
			if eventThreadID == threadID {
				t.Fatalf("未订阅客户端不应收到 %s: %s", event.Method, event.Params)
			}
		case <-timer.C:
			return
		}
	}
}

func protocolEventScope(raw json.RawMessage) (string, string) {
	var value struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		Turn     struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	_ = json.Unmarshal(raw, &value)
	if value.TurnID == "" {
		value.TurnID = value.Turn.ID
	}
	return value.ThreadID, value.TurnID
}

func waitForHubCollaborationMode(t *testing.T, events <-chan codex.Event, threadID, mode string) {
	t.Helper()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event, ok := <-events:
			require.True(t, ok, "App Server 在模式同步前退出")
			if event.Method != "thread/settings/updated" {
				continue
			}
			var value struct {
				ThreadID       string `json:"threadId"`
				ThreadSettings struct {
					CollaborationMode struct {
						Mode string `json:"mode"`
					} `json:"collaborationMode"`
				} `json:"threadSettings"`
			}
			require.NoError(t, json.Unmarshal(event.Params, &value))
			if value.ThreadID == threadID && value.ThreadSettings.CollaborationMode.Mode == mode {
				return
			}
		case <-timer.C:
			t.Fatalf("等待 collaboration mode %s 超时", mode)
		}
	}
}

func assertNoCollaborationMode(t *testing.T, events <-chan codex.Event, threadID, mode string,
	duration time.Duration,
) {
	t.Helper()
	timer := time.NewTimer(duration)
	defer timer.Stop()
	for {
		select {
		case event := <-events:
			if event.Method != "thread/settings/updated" {
				continue
			}
			var value struct {
				ThreadID       string `json:"threadId"`
				ThreadSettings struct {
					CollaborationMode struct {
						Mode string `json:"mode"`
					} `json:"collaborationMode"`
				} `json:"threadSettings"`
			}
			_ = json.Unmarshal(event.Params, &value)
			if value.ThreadID == threadID && value.ThreadSettings.CollaborationMode.Mode == mode {
				t.Fatalf("模式不应回退为 %s", mode)
			}
		case <-timer.C:
			return
		}
	}
}
