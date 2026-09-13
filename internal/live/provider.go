package live

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

var ErrNotConfigured = errors.New("Live API 未配置")
var ErrSessionExpired = errors.New("Live session 已失效")
var ErrSidebandBinary = errors.New("Live sideband 不允许二进制帧")
var ErrSidebandInvalidJSON = errors.New("Live sideband 返回了无效 JSON")

type SidebandBinaryError struct {
	Size int
}

func (e *SidebandBinaryError) Error() string {
	return fmt.Sprintf("%s（%d bytes）", ErrSidebandBinary, e.Size)
}

func (e *SidebandBinaryError) Unwrap() error { return ErrSidebandBinary }

type SessionConfig struct {
	Model        string         `json:"model"`
	Instructions string         `json:"instructions,omitempty"`
	Voice        string         `json:"-"`
	Input        []InputMessage `json:"input,omitempty"`
}
type InputMessage struct {
	Type    string         `json:"type"`
	Role    string         `json:"role"`
	Content []InputContent `json:"content"`
}
type InputContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
type SessionResult struct {
	ProviderSessionID string
	AnswerSDP         string
}

type Provider interface {
	CreateSession(context.Context, string, SessionConfig) (SessionResult, error)
	AttachSideband(context.Context, string) (Sideband, error)
}
type Sideband interface {
	ReadJSON(context.Context, any) error
	WriteJSON(context.Context, any) error
	Close() error
}

type HTTPProvider struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
	Dialer  *websocket.Dialer
}

func NewProvider(baseURL, apiKey string) *HTTPProvider {
	return &HTTPProvider{
		BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		APIKey:  strings.TrimSpace(apiKey),
		HTTP:    &http.Client{Timeout: 30 * time.Second},
		Dialer:  websocket.DefaultDialer,
	}
}
func (p *HTTPProvider) validate() error {
	if p == nil || p.BaseURL == "" || p.APIKey == "" {
		return ErrNotConfigured
	}
	u, err := url.ParseRequestURI(p.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("Live API Base URL 无效")
	}
	return nil
}
func (p *HTTPProvider) CreateSession(ctx context.Context, offer string, config SessionConfig) (SessionResult, error) {
	if err := p.validate(); err != nil {
		return SessionResult{}, err
	}
	if strings.TrimSpace(offer) == "" {
		return SessionResult{}, errors.New("SDP offer 不能为空")
	}
	session := map[string]any{"model": config.Model, "instructions": config.Instructions, "audio": map[string]any{"output": map[string]string{"voice": config.Voice}}}
	// Live create 拒绝未知字段 session.input；恢复历史改走 sideband session.context.append。
	payload, err := json.Marshal(map[string]any{"session": session, "transport": map[string]string{"type": "webrtc", "sdp": offer}})
	if err != nil {
		return SessionResult{}, fmt.Errorf("编码 Live session 请求: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/v1/live/sessions", bytes.NewReader(payload))
	if err != nil {
		return SessionResult{}, err
	}
	request.Header.Set("Authorization", "Bearer "+p.APIKey)
	request.Header.Set("Content-Type", "application/json")
	response, err := p.HTTP.Do(request)
	if err != nil {
		return SessionResult{}, fmt.Errorf("创建 Live session: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return SessionResult{}, fmt.Errorf("读取 Live session 响应: %w", err)
	}
	if response.StatusCode != http.StatusCreated {
		return SessionResult{}, errors.New(summarizeLiveProviderError(response.StatusCode, body))
	}
	var result struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
		Transport struct {
			SDP string `json:"sdp"`
		} `json:"transport"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return SessionResult{}, fmt.Errorf("解析 Live session 响应: %w", err)
	}
	if strings.TrimSpace(result.Session.ID) == "" {
		return SessionResult{}, errors.New("Live session 响应缺少 session.id")
	}
	if strings.TrimSpace(result.Transport.SDP) == "" {
		return SessionResult{}, errors.New("Live session 响应缺少 transport.sdp")
	}
	return SessionResult{ProviderSessionID: result.Session.ID, AnswerSDP: result.Transport.SDP}, nil
}
func (p *HTTPProvider) AttachSideband(ctx context.Context, sessionID string) (Sideband, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(sessionID) == "" {
		return nil, errors.New("remote session ID 不能为空")
	}
	u, err := url.Parse(p.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("解析 Live API Base URL: %w", err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return nil, errors.New("Live API Base URL 必须使用 HTTP 或 HTTPS")
	}
	basePath := strings.TrimRight(u.Path, "/")
	baseEscapedPath := strings.TrimRight(u.EscapedPath(), "/")
	u.Path = basePath + "/v1/live/sessions/" + sessionID + "/attach"
	u.RawPath = baseEscapedPath + "/v1/live/sessions/" + url.PathEscape(sessionID) + "/attach"
	dialer := p.Dialer
	if dialer == nil {
		dialer = websocket.DefaultDialer
	}
	connection, response, err := dialer.DialContext(ctx, u.String(), http.Header{
		"Authorization": []string{"Bearer " + p.APIKey},
	})
	if err != nil {
		if response != nil && (response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusGone) {
			return nil, fmt.Errorf("%w: HTTP %d", ErrSessionExpired, response.StatusCode)
		}
		return nil, fmt.Errorf("连接 Live sideband: %w", err)
	}
	connection.SetReadLimit(2 << 20)
	return &websocketSideband{connection: connection}, nil
}

type websocketSideband struct{ connection *websocket.Conn }

func (s *websocketSideband) ReadJSON(ctx context.Context, value any) error {
	if deadline, ok := ctx.Deadline(); ok {
		_ = s.connection.SetReadDeadline(deadline)
	}
	messageType, payload, err := s.connection.ReadMessage()
	if err != nil {
		return err
	}
	if messageType != websocket.TextMessage {
		return &SidebandBinaryError{Size: len(payload)}
	}
	if err := json.Unmarshal(payload, value); err != nil {
		return fmt.Errorf("%w: %v", ErrSidebandInvalidJSON, err)
	}
	return nil
}
func (s *websocketSideband) WriteJSON(ctx context.Context, value any) error {
	if deadline, ok := ctx.Deadline(); ok {
		_ = s.connection.SetWriteDeadline(deadline)
	}
	return s.connection.WriteJSON(value)
}
func (s *websocketSideband) Close() error { return s.connection.Close() }

func summarizeLiveProviderError(status int, body []byte) string {
	message := liveProviderErrorMessage(body)
	if message == "" {
		return fmt.Sprintf("Live session 返回 HTTP %d", status)
	}
	return fmt.Sprintf("Live session 返回 HTTP %d: %s", status, message)
}

func liveProviderErrorMessage(body []byte) string {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return ""
	}
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
			Param   string `json:"param"`
		} `json:"error"`
		Detail  string `json:"detail"`
		Message string `json:"message"`
	}
	if json.Unmarshal(trimmed, &payload) != nil {
		return ""
	}
	parts := make([]string, 0, 4)
	for _, value := range []string{payload.Error.Type, payload.Error.Code, payload.Error.Param} {
		value = strings.TrimSpace(value)
		if value != "" {
			parts = append(parts, value)
		}
	}
	text := strings.TrimSpace(payload.Error.Message)
	if text == "" {
		text = strings.TrimSpace(payload.Detail)
	}
	if text == "" {
		text = strings.TrimSpace(payload.Message)
	}
	if text != "" {
		parts = append(parts, text)
	}
	if len(parts) == 0 {
		return ""
	}
	summary := strings.Join(parts, ": ")
	summary = strings.ReplaceAll(summary, "\n", " ")
	summary = strings.ReplaceAll(summary, "\r", " ")
	runes := []rune(summary)
	if len(runes) > 300 {
		return string(runes[:300])
	}
	return summary
}
