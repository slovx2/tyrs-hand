//go:build integration

package protocol

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/appserverhub"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/stretchr/testify/require"
)

const onePixelPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="

func TestRealCodexHubPreservesLocalImageForResponsesWorkerAndDesktop(t *testing.T) {
	requests := make(chan []byte, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter,
		request *http.Request,
	) {
		body, _ := io.ReadAll(request.Body)
		requests <- body
		response.Header().Set("Content-Type", "text/event-stream")
		response.Header().Set("Cache-Control", "no-cache")
		_, _ = fmt.Fprint(response, sse(
			map[string]any{"type": "response.created", "response": map[string]any{"id": "image-response"}},
			map[string]any{"type": "response.output_item.done", "item": map[string]any{
				"type": "message", "role": "assistant", "id": "image-answer",
				"content": []map[string]any{{"type": "output_text", "text": "图片已收到。"}},
			}}, completedResponse("image-response")))
	}))
	t.Cleanup(upstream.Close)

	hub, workspace := startRealHub(t, upstream.URL, nil)
	imageBytes, err := base64.StdEncoding.DecodeString(onePixelPNG)
	require.NoError(t, err)
	imagePath := filepath.Join(workspace, "user-image.png")
	require.NoError(t, os.WriteFile(imagePath, imageBytes, 0o600))

	worker, err := hub.OpenClient(appserverhub.ClientOptions{Role: appserverhub.RoleWorker})
	require.NoError(t, err)
	t.Cleanup(func() { _ = worker.Close() })
	desktop, err := codex.ConnectSocket(context.Background(), codex.SocketClientOptions{
		SocketPath: hub.SocketPath(), ServerRequestTimeout: 30 * time.Second,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = desktop.Close() })
	var thread struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	require.NoError(t, desktop.Call(context.Background(), "thread/start", map[string]any{
		"cwd": workspace, "model": "mock-model", "approvalPolicy": "never", "sandbox": "read-only",
	}, &thread))
	threadID := thread.Thread.ID
	workerEvents := worker.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	desktopEvents := desktop.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	t.Cleanup(workerEvents.Close)
	t.Cleanup(desktopEvents.Close)

	var started struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	require.NoError(t, desktop.Call(context.Background(), "turn/start", map[string]any{
		"threadId": threadID,
		"input": []map[string]any{{"type": "text", "text": "描述图片", "textElements": []any{}},
			{"type": "localImage", "path": imagePath, "detail": "high"}},
	}, &started))
	require.NotEmpty(t, started.Turn.ID)

	var upstreamRequest []byte
	select {
	case upstreamRequest = <-requests:
	case <-time.After(10 * time.Second):
		t.Fatal("没有收到包含用户图片的 Responses 请求")
	}
	waitForLocalImage(t, workerEvents.Events(), threadID, started.Turn.ID, imagePath)
	waitForLocalImage(t, desktopEvents.Events(), threadID, started.Turn.ID, imagePath)
	require.Contains(t, string(upstreamRequest), `"type":"input_image"`)
	require.Contains(t, string(upstreamRequest), "data:image/png;base64,")
}

func TestRealCodexImageGenerationSchemaIncludesSavedPath(t *testing.T) {
	output := filepath.Join(t.TempDir(), "schema")
	command := exec.Command(fixedCodexBinary(t), "app-server", "generate-json-schema", "--out", output)
	data, err := command.CombinedOutput()
	require.NoError(t, err, string(data))
	raw, err := os.ReadFile(filepath.Join(output, "v2", "ItemCompletedNotification.json"))
	require.NoError(t, err)
	var schema any
	require.NoError(t, json.Unmarshal(raw, &schema))
	require.True(t, hasImageGenerationSavedPath(schema),
		"真实 Codex item/completed schema 必须声明 imageGeneration.savedPath")
}

func hasImageGenerationSavedPath(value any) bool {
	switch item := value.(type) {
	case map[string]any:
		if item["title"] == "ImageGenerationThreadItem" {
			properties, _ := item["properties"].(map[string]any)
			_, present := properties["savedPath"]
			return present
		}
		for _, child := range item {
			if hasImageGenerationSavedPath(child) {
				return true
			}
		}
	case []any:
		for _, child := range item {
			if hasImageGenerationSavedPath(child) {
				return true
			}
		}
	}
	return false
}

func waitForLocalImage(t *testing.T, events <-chan codex.Event, threadID, turnID,
	expectedPath string,
) {
	t.Helper()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	found := false
	for {
		select {
		case event, ok := <-events:
			require.True(t, ok, "App Server 在图片 Turn 完成前退出")
			if event.Method == "item/completed" || event.Method == "item/started" {
				var value struct {
					ThreadID string `json:"threadId"`
					TurnID   string `json:"turnId"`
					Item     struct {
						Type    string `json:"type"`
						Content []struct {
							Type   string `json:"type"`
							Path   string `json:"path"`
							Detail string `json:"detail"`
						} `json:"content"`
					} `json:"item"`
				}
				require.NoError(t, json.Unmarshal(event.Params, &value))
				if value.ThreadID == threadID && value.TurnID == turnID && value.Item.Type == "userMessage" {
					for _, content := range value.Item.Content {
						if content.Type == "localImage" && content.Path == expectedPath && content.Detail == "high" {
							found = true
						}
					}
				}
			}
			if event.Method == "turn/completed" {
				eventThreadID, eventTurnID := protocolEventScope(event.Params)
				if eventThreadID == threadID && eventTurnID == turnID {
					require.True(t, found, "Hub 没有投影用户 localImage")
					return
				}
			}
		case <-timer.C:
			t.Fatal("等待真实图片协议事件超时")
		}
	}
}
