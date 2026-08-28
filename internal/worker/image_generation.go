package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/ports"
	"go.uber.org/zap"
)

const (
	generatedImagesDirectory    = "generated-images"
	defaultImageGenerationModel = "gpt-image-2.5"
	imageGenerationModelLimit   = 256
	imageGenerationTimeout      = 180 * time.Second
	generatedImageFileLimit     = 10 << 20
	generatedImageRetention     = 7 * 24 * time.Hour
	imageResponseBodyLimit      = 16 << 20
	imageGenerationLockFile     = ".generating"
)

type imageGenerationReservationState int

const (
	imageGenerationReserved imageGenerationReservationState = iota
	imageGenerationInProgress
	imageGenerationAlreadySucceeded
)

type imageGenerationArguments struct {
	Prompt  string `json:"prompt"`
	Model   string `json:"model"`
	Size    string `json:"size"`
	Quality string `json:"quality"`
}

type imageGenerationResponse struct {
	Data []struct {
		B64JSON string `json:"b64_json"`
	} `json:"data"`
}

func imageGenerationSpec() ports.DynamicToolSpec {
	return ports.DynamicToolSpec{
		Type: "function", Name: "generate_image",
		Description: "Generate at most one PNG image in the current turn and attach it to the response. A successful result includes an exact Markdown image reference for Desktop compatibility. Copy that Markdown verbatim into the final answer, never claim the generation failed, and do not call this tool again in the same turn.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"prompt":{"type":"string","minLength":1,"maxLength":32000},"model":{"type":"string","minLength":1,"maxLength":256,"default":"gpt-image-2.5"},"size":{"type":"string","enum":["1024x1024","1024x1536","1536x1024"],"default":"1024x1024"},"quality":{"type":"string","enum":["auto","low","medium","high"],"default":"auto"}},"required":["prompt"],"additionalProperties":false}`),
	}
}

func withImageGenerationTool(specs ...ports.DynamicToolSpec) []ports.DynamicToolSpec {
	return append(specs, imageGenerationSpec())
}

func (p *Processor) executeImageGenerationTool(ctx context.Context, workspace string,
	request codex.ToolCallRequest,
) codex.ToolCallResult {
	arguments, err := parseImageGenerationArguments(request.Arguments)
	if err != nil {
		return imageGenerationFailure("图片生成参数无效，请检查 prompt、size 和 quality。")
	}
	if request.ThreadID == "" || request.TurnID == "" || request.CallID == "" {
		return imageGenerationFailure("图片生成调用缺少必要的会话信息。")
	}
	release, state, err := p.reserveImageGeneration(request.ThreadID, request.TurnID)
	if err != nil {
		return imageGenerationFailure("图片生成服务当前不可用。")
	}
	if state == imageGenerationAlreadySucceeded {
		return imageGenerationFailure("本轮已经成功生成一张图片，请直接向用户展示并回复，不要再次调用 generate_image。")
	}
	if state == imageGenerationInProgress {
		return imageGenerationFailure("本轮已有图片生成请求正在执行，请等待该结果并直接回复，不要重复调用 generate_image。")
	}
	defer release()
	if p.hostRuntime == nil && p.imageRuntime == nil {
		return imageGenerationFailure("图片生成服务当前不可用。")
	}
	runtime := p.imageRuntime
	if runtime == nil {
		runtime = codex.NewRuntime(p.hostRuntime.Client())
	}
	thread, err := runtime.ReadThread(ctx, request.ThreadID)
	if err != nil || strings.TrimSpace(thread.ModelProvider) == "" {
		return imageGenerationFailure("无法确定当前会话的图片服务 Provider。")
	}
	providerID := strings.TrimSpace(thread.ModelProvider)
	configCWD := strings.TrimSpace(thread.CWD)
	if configCWD == "" {
		configCWD = workspace
	}
	config, err := runtime.ReadRuntimeConfig(ctx, configCWD)
	if err != nil {
		return imageGenerationFailure("无法读取当前会话的图片服务配置。")
	}
	provider, found := config.ModelProviders[providerID]
	if !found || strings.TrimSpace(provider.BaseURL) == "" || strings.TrimSpace(provider.EnvKey) == "" {
		return imageGenerationFailure("当前会话 Provider 不支持图片生成。")
	}
	credential := strings.TrimSpace(os.Getenv(strings.TrimSpace(provider.EnvKey)))
	if credential == "" {
		return imageGenerationFailure("当前会话 Provider 的凭据未配置。")
	}
	endpoint, err := imageGenerationEndpoint(provider.BaseURL, provider.QueryParams)
	if err != nil {
		return imageGenerationFailure("当前会话 Provider 的图片服务地址无效。")
	}

	payload, _ := json.Marshal(map[string]any{
		"model": arguments.Model, "prompt": arguments.Prompt,
		"size": arguments.Size, "quality": arguments.Quality,
		"n": 1, "output_format": "png", "moderation": "low",
	})
	requestCtx, cancel := context.WithTimeout(ctx, p.imageRequestTimeout())
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(requestCtx, http.MethodPost,
		endpoint, bytes.NewReader(payload))
	if err != nil {
		return imageGenerationFailure("无法创建图片生成请求。")
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	for name, value := range provider.HTTPHeaders {
		httpRequest.Header.Set(name, value)
	}
	for name, environment := range provider.EnvHTTPHeaders {
		if value := strings.TrimSpace(os.Getenv(environment)); value != "" {
			httpRequest.Header.Set(name, value)
		}
	}
	httpRequest.Header.Set("Authorization", "Bearer "+credential)

	started := time.Now()
	statusCode, requestID := 0, ""
	defer func() {
		if p.logger != nil {
			p.logger.Info("图片生成请求完成", zap.String("provider_id", providerID),
				zap.Int("status_code", statusCode), zap.Duration("duration", time.Since(started)),
				zap.String("request_id", requestID))
		}
	}()
	response, err := p.imageHTTPClient().Do(httpRequest)
	if err != nil {
		if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
			return imageGenerationFailure("图片生成请求超时，请稍后重试。")
		}
		if errors.Is(requestCtx.Err(), context.Canceled) {
			return imageGenerationFailure("图片生成已取消。")
		}
		return imageGenerationFailure("图片服务暂时不可用，请稍后重试。")
	}
	defer func() { _ = response.Body.Close() }()
	statusCode = response.StatusCode
	requestID = imageRequestID(response.Header)
	body, err := io.ReadAll(io.LimitReader(response.Body, imageResponseBodyLimit+1))
	if err != nil || len(body) > imageResponseBodyLimit {
		return imageGenerationFailure("图片服务返回了无效响应。")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		if imageResponseWasModerated(body) {
			return imageGenerationFailure("图片请求未通过内容安全审核，请调整描述后重试。")
		}
		return imageGenerationFailure("图片服务请求失败，请稍后重试。")
	}
	var decoded imageGenerationResponse
	if json.Unmarshal(body, &decoded) != nil || len(decoded.Data) == 0 || decoded.Data[0].B64JSON == "" {
		return imageGenerationFailure("图片服务返回了无效响应。")
	}
	image, err := decodeGeneratedImage(decoded.Data[0].B64JSON)
	if err != nil {
		return imageGenerationFailure("图片服务返回了无效或过大的图片。")
	}
	path, err := p.writeGeneratedImage(request.ThreadID, request.TurnID, request.CallID, image)
	if err != nil {
		return imageGenerationFailure("生成的图片无法保存，请稍后重试。")
	}
	encoded := base64.StdEncoding.EncodeToString(image)
	return codex.ToolCallResult{Success: true, ContentItems: []codex.ToolContentItem{
		{Type: "inputImage", ImageURL: "data:image/png;base64," + encoded},
		{Type: "inputText", Text: fmt.Sprintf("SUCCESS: 图片已经生成。为兼容 Desktop，最终答复必须原样包含下面这一行 Markdown，不得声称生成失败；本轮不要再次调用 generate_image。\n![生成的图片](%s)", path)},
	}}
}

func parseImageGenerationArguments(raw json.RawMessage) (imageGenerationArguments, error) {
	var value imageGenerationArguments
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, err
	}
	if decoder.Decode(&struct{}{}) != io.EOF || value.Prompt == "" ||
		utf8.RuneCountInString(value.Prompt) > 32000 {
		return value, errors.New("图片参数不合法")
	}
	value.Model = strings.TrimSpace(value.Model)
	if value.Model == "" {
		value.Model = defaultImageGenerationModel
	}
	if utf8.RuneCountInString(value.Model) > imageGenerationModelLimit {
		return value, errors.New("图片模型名称不合法")
	}
	if value.Size == "" {
		value.Size = "1024x1024"
	}
	if value.Quality == "" {
		value.Quality = "auto"
	}
	if value.Size != "1024x1024" && value.Size != "1024x1536" && value.Size != "1536x1024" {
		return value, errors.New("图片尺寸不合法")
	}
	if value.Quality != "auto" && value.Quality != "low" && value.Quality != "medium" && value.Quality != "high" {
		return value, errors.New("图片质量不合法")
	}
	return value, nil
}

func imageGenerationFailure(message string) codex.ToolCallResult {
	return codex.TextToolResult(message, false)
}

func imageGenerationEndpoint(baseURL string, queryParams map[string]string) (string, error) {
	value, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || value.Scheme == "" || value.Host == "" {
		return "", errors.New("Provider base_url 无效")
	}
	value.Path = strings.TrimRight(value.Path, "/") + "/images/generations"
	query := value.Query()
	for name, item := range queryParams {
		query.Set(name, item)
	}
	value.RawQuery = query.Encode()
	return value.String(), nil
}

func imageRequestID(header http.Header) string {
	for _, name := range []string{"x-request-id", "request-id", "openai-request-id"} {
		if value := strings.TrimSpace(header.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

func imageResponseWasModerated(body []byte) bool {
	var value struct {
		Error struct {
			Code    string `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &value) != nil {
		return false
	}
	detail := strings.ToLower(value.Error.Code + " " + value.Error.Type + " " + value.Error.Message)
	return strings.Contains(detail, "content_policy") || strings.Contains(detail, "moderation") ||
		strings.Contains(detail, "safety")
}

func decodeGeneratedImage(encoded string) ([]byte, error) {
	if len(encoded) > base64.StdEncoding.EncodedLen(generatedImageFileLimit)+4 {
		return nil, errors.New("图片超过 10 MiB")
	}
	decoder := base64.NewDecoder(base64.StdEncoding, strings.NewReader(encoded))
	image, err := io.ReadAll(io.LimitReader(decoder, generatedImageFileLimit+1))
	if err != nil || len(image) == 0 || len(image) > generatedImageFileLimit {
		return nil, errors.New("图片 Base64 无效或超过 10 MiB")
	}
	if len(image) < 8 || !bytes.Equal(image[:8], []byte("\x89PNG\r\n\x1a\n")) {
		return nil, errors.New("图片不是 PNG")
	}
	return image, nil
}

func (p *Processor) imageHTTPClient() *http.Client {
	if p.imageHTTP != nil {
		return p.imageHTTP
	}
	return &http.Client{Timeout: imageGenerationTimeout}
}

func (p *Processor) imageRequestTimeout() time.Duration {
	if p.imageTimeout > 0 {
		return p.imageTimeout
	}
	return imageGenerationTimeout
}

func (p *Processor) currentImageTime() time.Time {
	if p.imageNow != nil {
		return p.imageNow()
	}
	return time.Now()
}

func (p *Processor) generatedImageRoot() (string, error) {
	if strings.TrimSpace(p.imageRoot) != "" {
		return filepath.Clean(p.imageRoot), nil
	}
	if p.hostRuntime == nil || strings.TrimSpace(p.hostRuntime.StateDir()) == "" {
		return "", errors.New("Worker 私有状态目录不可用")
	}
	return filepath.Join(p.hostRuntime.StateDir(), generatedImagesDirectory), nil
}

func generatedImageScope(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:16])
}

func (p *Processor) generatedImageTurnDirectory(threadID, turnID string) (string, error) {
	root, err := p.generatedImageRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, generatedImageScope(threadID), generatedImageScope(turnID)), nil
}

func (p *Processor) reserveImageGeneration(threadID, turnID string) (func(),
	imageGenerationReservationState, error,
) {
	directory, err := p.prepareGeneratedImageDirectory(threadID, turnID)
	if err != nil {
		return func() {}, imageGenerationReserved, err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return func() {}, imageGenerationReserved, err
	}
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".png" {
			return func() {}, imageGenerationAlreadySucceeded, nil
		}
	}
	lockPath := filepath.Join(directory, imageGenerationLockFile)
	lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return func() {}, imageGenerationInProgress, nil
	}
	if err != nil {
		return func() {}, imageGenerationReserved, err
	}
	if err := lock.Close(); err != nil {
		_ = os.Remove(lockPath)
		return func() {}, imageGenerationReserved, err
	}
	return func() { _ = os.Remove(lockPath) }, imageGenerationReserved, nil
}

func (p *Processor) prepareGeneratedImageDirectory(threadID, turnID string) (string, error) {
	directory, err := p.generatedImageTurnDirectory(threadID, turnID)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	root, err := p.generatedImageRoot()
	if err != nil {
		return "", err
	}
	for current := directory; pathInside(root, current); current = filepath.Dir(current) {
		if err := os.Chmod(current, 0o700); err != nil {
			return "", err
		}
		if current == root {
			break
		}
	}
	return directory, nil
}

func (p *Processor) writeGeneratedImage(threadID, turnID, callID string,
	image []byte,
) (string, error) {
	if err := p.cleanupStaleGeneratedImages(p.currentImageTime()); err != nil && p.logger != nil {
		p.logger.Warn("清理过期生成图片失败", zap.Error(err))
	}
	directory, err := p.prepareGeneratedImageDirectory(threadID, turnID)
	if err != nil {
		return "", err
	}
	path := filepath.Join(directory, generatedImageScope(callID)+".png")
	file, err := os.CreateTemp(directory, "."+generatedImageScope(callID)+"-*.tmp")
	if err != nil {
		return "", err
	}
	temporary := file.Name()
	_, writeErr := file.Write(image)
	closeErr := file.Close()
	if writeErr != nil {
		_ = os.Remove(temporary)
		return "", writeErr
	}
	if closeErr != nil {
		_ = os.Remove(temporary)
		return "", closeErr
	}
	if err := os.Chmod(temporary, 0o600); err != nil {
		_ = os.Remove(temporary)
		return "", err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return "", err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func (p *Processor) cleanupStaleGeneratedImages(now time.Time) error {
	root, err := p.generatedImageRoot()
	if err != nil {
		return err
	}
	threads, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	cutoff := now.Add(-generatedImageRetention)
	for _, thread := range threads {
		if !thread.IsDir() {
			continue
		}
		threadPath := filepath.Join(root, thread.Name())
		turns, readErr := os.ReadDir(threadPath)
		if readErr != nil {
			continue
		}
		for _, turn := range turns {
			if !turn.IsDir() {
				continue
			}
			turnPath := filepath.Join(threadPath, turn.Name())
			newest, timeErr := newestGeneratedImageTime(turnPath)
			if timeErr == nil && newest.Before(cutoff) {
				_ = os.RemoveAll(turnPath)
			}
		}
		if remaining, readErr := os.ReadDir(threadPath); readErr == nil && len(remaining) == 0 {
			_ = os.Remove(threadPath)
		}
	}
	return nil
}

func newestGeneratedImageTime(directory string) (time.Time, error) {
	info, err := os.Stat(directory)
	if err != nil {
		return time.Time{}, err
	}
	newest := info.ModTime()
	err = filepath.WalkDir(directory, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		item, statErr := entry.Info()
		if statErr != nil {
			return statErr
		}
		if item.ModTime().After(newest) {
			newest = item.ModTime()
		}
		return nil
	})
	return newest, err
}
