//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/ports"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestRealCodexAppServerWithMockResponsesAndDynamicTool(t *testing.T) {
	bin := fixedCodexBinary(t)
	var requestCount atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/v1/responses", request.URL.Path)
		require.Equal(t, http.MethodPost, request.Method)
		var requestBody map[string]any
		require.NoError(t, json.NewDecoder(request.Body).Decode(&requestBody))
		format := requestBody["text"].(map[string]any)["format"].(map[string]any)
		require.Equal(t, "json_schema", format["type"])
		response.Header().Set("Content-Type", "text/event-stream")
		response.Header().Set("Cache-Control", "no-cache")
		if requestCount.Add(1) == 1 {
			_, _ = fmt.Fprint(response, sse(
				map[string]any{"type": "response.created", "response": map[string]any{"id": "resp-1"}},
				map[string]any{"type": "response.output_item.done", "item": map[string]any{
					"type": "function_call", "call_id": "call-1", "namespace": "github", "name": "echo", "arguments": `{"message":"hello"}`,
				}},
				completedResponse("resp-1"),
			))
			return
		}
		_, _ = fmt.Fprint(response, sse(
			map[string]any{"type": "response.created", "response": map[string]any{"id": "resp-2"}},
			map[string]any{"type": "response.output_item.done", "item": map[string]any{
				"type": "message", "role": "assistant", "id": "msg-1", "content": []map[string]any{{"type": "output_text", "text": "done"}},
			}},
			completedResponse("resp-2"),
		))
	}))
	t.Cleanup(upstream.Close)

	root := temporaryDir(t, "tyrs-hand-codex-tools-")
	codexHome := filepath.Join(root, "codex-home")
	worktree := filepath.Join(root, "worktree")
	require.NoError(t, os.MkdirAll(filepath.Join(worktree, ".agents", "skills", "demo"), 0o755))
	require.NoError(t, os.MkdirAll(codexHome, 0o700))
	gitInit := exec.Command("git", "init", "-b", "main", worktree)
	require.NoError(t, gitInit.Run())
	skillPath := filepath.Join(worktree, ".agents", "skills", "demo", "SKILL.md")
	require.NoError(t, os.WriteFile(skillPath, []byte("---\nname: demo\ndescription: test skill\n---\nFollow the test instruction.\n"), 0o644))
	config := fmt.Sprintf(`model = "mock-model"
approval_policy = "never"
sandbox_mode = "read-only"
model_provider = "mock_provider"

[model_providers.mock_provider]
name = "Mock provider for integration test"
base_url = %q
wire_api = "responses"
request_max_retries = 0
stream_max_retries = 0
supports_websockets = false
`, upstream.URL+"/v1")
	require.NoError(t, os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(config), 0o600))

	toolCalled := make(chan codex.ToolCallRequest, 1)
	client, err := codex.Start(context.Background(), codex.ClientOptions{
		Bin: bin, CWD: worktree, CodexHome: codexHome, RequestTimeout: 30 * time.Second,
		ToolTimeout: 30 * time.Second, Logger: zap.NewNop(),
		ToolHandler: func(_ context.Context, request codex.ToolCallRequest) (codex.ToolCallResult, error) {
			toolCalled <- request
			return codex.TextToolResult("echo-ok", true), nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	runtime := codex.NewRuntime(client)
	skills := []ports.SkillRef{{Name: "demo", Path: skillPath}}
	require.NoError(t, runtime.ValidateSkills(context.Background(), worktree, skills))
	threadID, err := runtime.StartThread(context.Background(), ports.ThreadOptions{
		CWD: worktree, Model: "mock-model", Sandbox: "read-only", ApprovalPolicy: "never",
		DynamicTools: []ports.DynamicToolSpec{
			{
				Type: "namespace", Name: "github", Description: "test",
				Tools: []ports.DynamicToolSpec{
					{Type: "function", Name: "echo", Description: "echo", InputSchema: json.RawMessage(`{"type":"object","properties":{"message":{"type":"string"}},"required":["message"],"additionalProperties":false}`)},
				},
			},
		},
	})
	require.NoError(t, err)
	turnID, err := runtime.StartTurn(context.Background(), threadID, ports.TurnInput{
		Text: "Call the echo tool.", Skills: skills,
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"result":{"type":"string"}},"required":["result"],"additionalProperties":false}`),
	})
	require.NoError(t, err)
	waitForCompletedTurn(t, client.Events(), threadID, turnID)
	select {
	case call := <-toolCalled:
		require.Equal(t, "github", *call.Namespace)
		require.Equal(t, "echo", call.Tool)
	case <-time.After(5 * time.Second):
		t.Fatal("没有收到真实 App Server 的 dynamic tool 回调")
	}
	require.GreaterOrEqual(t, requestCount.Load(), int32(2))
}

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
		t.Skip("本机没有安装 Codex 0.142.5")
	}
	output, err := exec.Command(path, "--version").CombinedOutput()
	require.NoError(t, err)
	require.Equal(t, "codex-cli 0.142.5", strings.TrimSpace(string(output)))
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

func waitForCompletedTurn(t *testing.T, events <-chan codex.Event, threadID, turnID string) {
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
			var value struct {
				ThreadID string `json:"threadId"`
				Turn     struct {
					ID string `json:"id"`
				} `json:"turn"`
			}
			require.NoError(t, json.Unmarshal(event.Params, &value))
			if value.ThreadID == threadID && value.Turn.ID == turnID {
				return
			}
		case <-timer.C:
			t.Fatal("等待真实 Codex Turn 完成超时")
		}
	}
}
