package worker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type imageRuntimeClient struct {
	providerID string
	config     codex.RuntimeConfig
	methods    []string
	configCWD  string
}

type imageRoundTripFunc func(*http.Request) (*http.Response, error)

func (function imageRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func (c *imageRuntimeClient) Call(_ context.Context, method string, payload any, result any) error {
	c.methods = append(c.methods, method)
	var value any
	switch method {
	case "thread/read":
		value = map[string]any{"thread": map[string]any{
			"id": "thread-1", "cwd": "/thread/workspace", "modelProvider": c.providerID,
		}}
	case "config/read":
		c.configCWD, _ = payload.(map[string]any)["cwd"].(string)
		value = map[string]any{"config": c.config}
	}
	encoded, _ := json.Marshal(value)
	return json.Unmarshal(encoded, result)
}

func TestGenerateImageUsesThreadProviderAndPersistsPrivatePNG(t *testing.T) {
	image, err := base64.StdEncoding.DecodeString(agentImageTestPNG)
	require.NoError(t, err)
	var requestBody map[string]any
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		require.Equal(t, "/v1/images/generations", request.URL.Path)
		require.Equal(t, "tenant-a", request.URL.Query().Get("tenant"))
		require.Equal(t, "Bearer provider-secret", request.Header.Get("Authorization"))
		require.Equal(t, "static-value", request.Header.Get("X-Static"))
		require.Equal(t, "environment-value", request.Header.Get("X-Environment"))
		require.NoError(t, json.NewDecoder(request.Body).Decode(&requestBody))
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-request-id", "image-request-1")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{
			{"b64_json": base64.StdEncoding.EncodeToString(image)},
		}})
	}))
	defer server.Close()
	t.Setenv("TEST_IMAGE_KEY", "provider-secret")
	t.Setenv("TEST_IMAGE_HEADER", "environment-value")
	runtimeClient := &imageRuntimeClient{providerID: "thread-provider",
		config: codex.RuntimeConfig{ModelProvider: "worker-default",
			ModelProviders: map[string]codex.ModelProvider{
				"thread-provider": {
					BaseURL: server.URL + "/v1", EnvKey: "TEST_IMAGE_KEY",
					HTTPHeaders: map[string]string{"X-Static": "static-value"},
					EnvHTTPHeaders: map[string]string{
						"X-Environment": "TEST_IMAGE_HEADER",
					},
					QueryParams: map[string]string{"tenant": "tenant-a"},
				},
			}},
	}
	root := filepath.Join(t.TempDir(), "generated-images")
	processor := &Processor{imageRuntime: codex.NewRuntime(runtimeClient),
		imageHTTP: server.Client(), imageRoot: root, logger: zap.NewNop()}
	result := processor.executeImageGenerationTool(context.Background(), t.TempDir(),
		codex.ToolCallRequest{ThreadID: "thread-1", TurnID: "turn-1", CallID: "call-1",
			Tool: "generate_image", Arguments: json.RawMessage(
				`{"prompt":"一只猫","model":"provider-image-preview","size":"1024x1536","quality":"high"}`)})

	require.True(t, result.Success)
	require.Len(t, result.ContentItems, 2)
	require.Equal(t, "inputImage", result.ContentItems[0].Type)
	require.Equal(t, "data:image/png;base64,"+agentImageTestPNG,
		result.ContentItems[0].ImageURL)
	require.Equal(t, "inputText", result.ContentItems[1].Type)
	require.Contains(t, result.ContentItems[1].Text, "SUCCESS")
	require.Contains(t, result.ContentItems[1].Text, "![生成的图片]")
	require.Equal(t, "provider-image-preview", requestBody["model"])
	require.Equal(t, "1024x1536", requestBody["size"])
	require.Equal(t, "high", requestBody["quality"])
	require.Equal(t, float64(1), requestBody["n"])
	require.Equal(t, "png", requestBody["output_format"])
	require.Equal(t, []string{"thread/read", "config/read"}, runtimeClient.methods)
	require.Equal(t, "/thread/workspace", runtimeClient.configCWD)

	directory, err := processor.generatedImageTurnDirectory("thread-1", "turn-1")
	require.NoError(t, err)
	path := filepath.Join(directory, generatedImageScope("call-1")+".png")
	stored, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, image, stored)
	fileInfo, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), fileInfo.Mode().Perm())
	for _, directory := range []string{root, filepath.Dir(directory), directory} {
		info, statErr := os.Stat(directory)
		require.NoError(t, statErr)
		require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	}

	duplicate := processor.executeImageGenerationTool(context.Background(), t.TempDir(),
		codex.ToolCallRequest{ThreadID: "thread-1", TurnID: "turn-1", CallID: "call-2",
			Tool: "generate_image", Arguments: json.RawMessage(`{"prompt":"再生成一次"}`)})
	require.False(t, duplicate.Success)
	require.Contains(t, duplicate.ContentItems[0].Text, "本轮已经成功")
	require.EqualValues(t, 1, requests.Load())
}

func TestGenerateImageRejectsInvalidConfigurationAndArguments(t *testing.T) {
	tests := []struct {
		name       string
		providerID string
		provider   codex.ModelProvider
		arguments  string
		contains   string
	}{
		{name: "invalid arguments", providerID: "provider", arguments: `{"prompt":"","extra":1}`,
			contains: "参数无效"},
		{name: "invalid model", providerID: "provider",
			arguments: `{"prompt":"cat","model":"` + strings.Repeat("m", imageGenerationModelLimit+1) + `"}`,
			contains:  "参数无效"},
		{name: "missing provider", providerID: "missing", arguments: `{"prompt":"cat"}`,
			contains: "不支持图片生成"},
		{name: "missing env", providerID: "provider", arguments: `{"prompt":"cat"}`,
			provider: codex.ModelProvider{BaseURL: "https://example.invalid/v1", EnvKey: "MISSING_IMAGE_KEY"},
			contains: "凭据未配置"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &imageRuntimeClient{providerID: test.providerID,
				config: codex.RuntimeConfig{ModelProviders: map[string]codex.ModelProvider{
					"provider": test.provider,
				}}}
			processor := &Processor{imageRuntime: codex.NewRuntime(client),
				imageRoot: filepath.Join(t.TempDir(), "images")}
			result := processor.executeImageGenerationTool(context.Background(), t.TempDir(),
				codex.ToolCallRequest{ThreadID: "thread", TurnID: "turn", CallID: "call",
					Arguments: json.RawMessage(test.arguments)})
			require.False(t, result.Success)
			require.Contains(t, result.ContentItems[0].Text, test.contains)
		})
	}
}

func TestGenerateImageAllowsProviderModelOverride(t *testing.T) {
	arguments, err := parseImageGenerationArguments(json.RawMessage(
		`{"prompt":"cat","model":" provider-image-preview "}`))
	require.NoError(t, err)
	require.Equal(t, "provider-image-preview", arguments.Model)

	arguments, err = parseImageGenerationArguments(json.RawMessage(`{"prompt":"cat"}`))
	require.NoError(t, err)
	require.Equal(t, defaultImageGenerationModel, arguments.Model)
}

func TestGenerateImageReturnsSanitizedUpstreamFailures(t *testing.T) {
	tests := []struct {
		name, body, contains string
		status               int
	}{
		{name: "moderation", status: http.StatusBadRequest,
			body:     `{"error":{"code":"content_policy_violation","message":"secret prompt"}}`,
			contains: "内容安全审核"},
		{name: "upstream", status: http.StatusBadGateway, body: `private response body`,
			contains: "请求失败"},
		{name: "malformed success", status: http.StatusOK, body: `{"data":[]}`,
			contains: "无效响应"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			t.Setenv("FAILURE_IMAGE_KEY", "secret")
			client := &imageRuntimeClient{providerID: "provider",
				config: codex.RuntimeConfig{ModelProviders: map[string]codex.ModelProvider{
					"provider": {BaseURL: server.URL, EnvKey: "FAILURE_IMAGE_KEY"},
				}}}
			processor := &Processor{imageRuntime: codex.NewRuntime(client),
				imageHTTP: server.Client(), imageRoot: filepath.Join(t.TempDir(), "images")}
			result := processor.executeImageGenerationTool(context.Background(), t.TempDir(),
				codex.ToolCallRequest{ThreadID: "thread", TurnID: "turn", CallID: "call",
					Arguments: json.RawMessage(`{"prompt":"never log this"}`)})
			require.False(t, result.Success)
			require.Contains(t, result.ContentItems[0].Text, test.contains)
			require.NotContains(t, result.ContentItems[0].Text, "secret")
			require.NotContains(t, result.ContentItems[0].Text, "private")
		})
	}
}

func TestGenerateImageHandlesOversizeAndCancellation(t *testing.T) {
	oversized := base64.StdEncoding.EncodeToString(make([]byte, generatedImageFileLimit+1))
	_, err := decodeGeneratedImage(oversized)
	require.ErrorContains(t, err, "10 MiB")
	_, err = decodeGeneratedImage(base64.StdEncoding.EncodeToString([]byte("not png")))
	require.ErrorContains(t, err, "不是 PNG")

	t.Setenv("CANCELED_IMAGE_KEY", "secret")
	client := &imageRuntimeClient{providerID: "provider",
		config: codex.RuntimeConfig{ModelProviders: map[string]codex.ModelProvider{
			"provider": {BaseURL: "https://example.invalid/v1", EnvKey: "CANCELED_IMAGE_KEY"},
		}}}
	processor := &Processor{imageRuntime: codex.NewRuntime(client),
		imageRoot: filepath.Join(t.TempDir(), "images")}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := processor.executeImageGenerationTool(ctx, t.TempDir(), codex.ToolCallRequest{
		ThreadID: "thread", TurnID: "turn", CallID: "call",
		Arguments: json.RawMessage(`{"prompt":"cat"}`),
	})
	require.False(t, result.Success)
	require.Contains(t, result.ContentItems[0].Text, "取消")

	client.providerID = "provider"
	client.config.ModelProviders["provider"] = codex.ModelProvider{
		BaseURL: "https://example.invalid/v1", EnvKey: "CANCELED_IMAGE_KEY",
	}
	processor.imageHTTP = &http.Client{Transport: imageRoundTripFunc(
		func(request *http.Request) (*http.Response, error) {
			<-request.Context().Done()
			return nil, request.Context().Err()
		})}
	processor.imageTimeout = 20 * time.Millisecond
	result = processor.executeImageGenerationTool(context.Background(), t.TempDir(),
		codex.ToolCallRequest{ThreadID: "thread", TurnID: "turn", CallID: "call",
			Arguments: json.RawMessage(`{"prompt":"cat"}`)})
	require.False(t, result.Success)
	require.Contains(t, result.ContentItems[0].Text, "超时")
}

func TestGeneratedImageRecoveryScanCleanupAndRetention(t *testing.T) {
	image, err := base64.StdEncoding.DecodeString(agentImageTestPNG)
	require.NoError(t, err)
	now := time.Date(2026, time.August, 18, 12, 0, 0, 0, time.UTC)
	processor := &Processor{imageRoot: filepath.Join(t.TempDir(), "generated-images"),
		imageNow: func() time.Time { return now }, logger: zap.NewNop()}
	_, err = processor.writeGeneratedImage("thread", "turn", "call", image)
	require.NoError(t, err)

	candidates, err := processor.generatedImageCandidates("thread", "turn")
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	require.Equal(t, "generate-image-"+generatedImageScope("call"), candidates[0].itemID)
	require.FileExists(t, candidates[0].path)

	processor.cleanupGeneratedImageTurn("thread", "turn")
	require.NoFileExists(t, candidates[0].path)

	_, err = processor.writeGeneratedImage("old-thread", "old-turn", "old-call", image)
	require.NoError(t, err)
	oldDirectory, err := processor.generatedImageTurnDirectory("old-thread", "old-turn")
	require.NoError(t, err)
	oldTime := now.Add(-generatedImageRetention - time.Hour)
	entries, err := os.ReadDir(oldDirectory)
	require.NoError(t, err)
	for _, entry := range entries {
		require.NoError(t, os.Chtimes(filepath.Join(oldDirectory, entry.Name()), oldTime, oldTime))
	}
	require.NoError(t, os.Chtimes(oldDirectory, oldTime, oldTime))
	require.NoError(t, processor.cleanupStaleGeneratedImages(now))
	_, err = os.Stat(oldDirectory)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestImageGenerationSpecMatchesPublicContract(t *testing.T) {
	spec := imageGenerationSpec()
	require.Equal(t, "function", spec.Type)
	require.Equal(t, "generate_image", spec.Name)
	var schema struct {
		Required             []string `json:"required"`
		AdditionalProperties bool     `json:"additionalProperties"`
		Properties           struct {
			Prompt struct {
				MinLength int `json:"minLength"`
				MaxLength int `json:"maxLength"`
			} `json:"prompt"`
			Model struct {
				MinLength int    `json:"minLength"`
				MaxLength int    `json:"maxLength"`
				Default   string `json:"default"`
			} `json:"model"`
			Size struct {
				Enum    []string `json:"enum"`
				Default string   `json:"default"`
			} `json:"size"`
			Quality struct {
				Enum    []string `json:"enum"`
				Default string   `json:"default"`
			} `json:"quality"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(spec.InputSchema, &schema))
	require.Equal(t, []string{"prompt"}, schema.Required)
	require.False(t, schema.AdditionalProperties)
	require.Equal(t, 1, schema.Properties.Prompt.MinLength)
	require.Equal(t, 32000, schema.Properties.Prompt.MaxLength)
	require.Equal(t, 1, schema.Properties.Model.MinLength)
	require.Equal(t, imageGenerationModelLimit, schema.Properties.Model.MaxLength)
	require.Equal(t, defaultImageGenerationModel, schema.Properties.Model.Default)
	require.Equal(t, "1024x1024", schema.Properties.Size.Default)
	require.Equal(t, []string{"auto", "low", "medium", "high"}, schema.Properties.Quality.Enum)
}
