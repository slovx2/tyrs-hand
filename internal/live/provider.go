package live

import (
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
	return &HTTPProvider{BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"), APIKey: strings.TrimSpace(apiKey), HTTP: &http.Client{Timeout: 30 * time.Second}, Dialer: websocket.DefaultDialer}
}
func (p *HTTPProvider) validate() error {
	if p == nil || p.BaseURL == "" || p.APIKey == "" {
		return ErrNotConfigured
	}
	u, err := url.ParseRequestURI(p.BaseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
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
	if len(config.Input) > 0 {
		session["input"] = config.Input
	}
	payload, err := json.Marshal(map[string]any{"session": session, "transport": map[string]string{"type": "webrtc", "sdp": offer}})
	if err != nil {
		return SessionResult{}, fmt.Errorf("编码 Live session 请求: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/v1/live/sessions", strings.NewReader(string(payload)))
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
		return SessionResult{}, fmt.Errorf("Live session 返回 HTTP %d", response.StatusCode)
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
	connection, response, err := p.Dialer.DialContext(ctx, u.String(), http.Header{"Authorization": []string{"Bearer " + p.APIKey}})
	if err != nil {
		if response != nil && (response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusGone) {
			return nil, fmt.Errorf("%w: HTTP %d", ErrSessionExpired, response.StatusCode)
		}
		return nil, fmt.Errorf("连接 Live sideband: %w", err)
	}
	return &websocketSideband{connection: connection}, nil
}

type websocketSideband struct{ connection *websocket.Conn }

func (s *websocketSideband) ReadJSON(ctx context.Context, value any) error {
	if deadline, ok := ctx.Deadline(); ok {
		_ = s.connection.SetReadDeadline(deadline)
	}
	return s.connection.ReadJSON(value)
}
func (s *websocketSideband) WriteJSON(ctx context.Context, value any) error {
	if deadline, ok := ctx.Deadline(); ok {
		_ = s.connection.SetWriteDeadline(deadline)
	}
	return s.connection.WriteJSON(value)
}
func (s *websocketSideband) Close() error { return s.connection.Close() }
