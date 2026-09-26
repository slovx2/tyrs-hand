package appserverhub

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
)

type oauthCallback struct {
	owner    int64
	name     string
	threadID string
	callback *url.URL
	address  string
	expires  time.Time
}

// 只登记真实上游返回的单次 OAuth 回调；该表不授权任意端口或通用 TCP 转发。
func (r *Hub) registerOAuthCallback(source *session, params, result json.RawMessage) error {
	var input struct {
		Name        string
		ThreadID    string
		TimeoutSecs *int
	}
	var output struct{ AuthorizationURL string }
	if json.Unmarshal(params, &input) != nil || json.Unmarshal(result, &output) != nil {
		return errors.New("OAuth 返回格式无效")
	}
	authorization, err := url.Parse(output.AuthorizationURL)
	if err != nil {
		return errors.New("OAuth 授权地址无效")
	}
	callback, err := url.Parse(authorization.Query().Get("redirect_uri"))
	if err != nil || callback.Scheme != "http" || callback.User != nil || callback.Fragment != "" || callback.RawQuery != "" {
		return errors.New("OAuth 回调必须是无查询参数的回环 HTTP 地址")
	}
	address, ok := oauthLoopbackAddress(callback.Hostname(), callback.Port())
	state := authorization.Query().Get("state")
	if !ok || len(state) < 16 || len(state) > 2048 || len(authorization.Query()["state"]) != 1 || callback.Path == "" {
		return errors.New("OAuth 回调缺少有效地址或随机状态")
	}
	timeout := 300
	if input.TimeoutSecs != nil {
		timeout = *input.TimeoutSecs
	}
	if timeout < 1 || timeout > 3600 {
		return errors.New("OAuth 回调超时无效")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.sessions[source.id] != source {
		return errSessionClosed
	}
	select {
	case <-source.done:
		return errSessionClosed
	default:
	}
	if r.oauthCallbacks == nil {
		r.oauthCallbacks = make(map[string]oauthCallback)
	}
	r.pruneOAuthCallbacksLocked()
	r.oauthCallbacks[state] = oauthCallback{owner: source.id, name: input.Name, threadID: input.ThreadID, callback: callback, address: address, expires: time.Now().Add(time.Duration(timeout) * time.Second)}
	return nil
}

func oauthLoopbackAddress(host, port string) (string, bool) {
	parsed, err := strconv.Atoi(port)
	if err != nil || parsed < 1 || parsed > 65535 {
		return "", false
	}
	switch host {
	case "127.0.0.1", "localhost":
		return net.JoinHostPort("127.0.0.1", port), true
	case "::1":
		return net.JoinHostPort("::1", port), true
	default:
		return "", false
	}
}

func (r *Hub) pruneOAuthCallbacksLocked() {
	for state, flow := range r.oauthCallbacks {
		if time.Now().After(flow.expires) || r.sessions[flow.owner] == nil {
			delete(r.oauthCallbacks, state)
		}
	}
}

func (r *Hub) OAuthCallbackAllowed(host string, port uint32) bool {
	address, ok := oauthLoopbackAddress(host, strconv.FormatUint(uint64(port), 10))
	if !ok {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneOAuthCallbacksLocked()
	if r.closed {
		return false
	}
	for _, flow := range r.oauthCallbacks {
		if flow.address == address {
			return true
		}
	}
	return false
}

// 解析并核对一条 HTTP GET 再转发，不能通过获准端口访问别的路径或发送任意数据。
func (r *Hub) ServeOAuthCallback(ctx context.Context, host string, port uint32, stream io.ReadWriteCloser) error {
	defer func() { _ = stream.Close() }()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = stream.Close() })
	defer stop()
	address, ok := oauthLoopbackAddress(host, strconv.FormatUint(uint64(port), 10))
	if !ok {
		return errors.New("禁止非回环 OAuth 目标")
	}
	request, err := http.ReadRequest(bufio.NewReader(io.LimitReader(stream, 16<<10)))
	if err != nil {
		return errors.New("OAuth 回调 HTTP 无效")
	}
	defer func() { _ = request.Body.Close() }()
	reject := func() error {
		_, _ = io.WriteString(stream, "HTTP/1.1 403 Forbidden\r\nConnection: close\r\nContent-Length: 0\r\n\r\n")
		return errors.New("OAuth 回调不属于有效单次流程")
	}
	if request.Method != http.MethodGet || request.ContentLength > 0 || len(request.TransferEncoding) > 0 || request.URL.IsAbs() {
		return reject()
	}
	values, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil || len(values["state"]) != 1 || (len(values["code"]) != 1 && len(values["error"]) != 1) {
		return reject()
	}
	r.mu.Lock()
	r.pruneOAuthCallbacksLocked()
	flow, valid := r.oauthCallbacks[values.Get("state")]
	owner := r.sessions[flow.owner]
	valid = valid && !r.closed && owner != nil && flow.address == address && flow.callback.Path == request.URL.Path
	if valid {
		delete(r.oauthCallbacks, values.Get("state"))
	}
	r.mu.Unlock()
	if !valid {
		return reject()
	}
	// 已消费的 flow 仍受原会话与 Hub 生命周期约束，不能等 HTTP 超时才退出。
	ctx, stopFlow := context.WithDeadline(ctx, flow.expires)
	defer stopFlow()
	go func() {
		select {
		case <-owner.done:
			cancel()
		case <-r.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	destination := *flow.callback
	destination.RawQuery = request.URL.RawQuery
	forwarded, err := http.NewRequestWithContext(ctx, http.MethodGet, destination.String(), nil)
	if err != nil {
		return err
	}
	transport := &http.Transport{
		Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 25 * time.Second,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", flow.address)
		},
	}
	defer transport.CloseIdleConnections()
	response, err := transport.RoundTrip(forwarded)
	if err != nil {
		return errors.New("OAuth 回调服务不可用")
	}
	defer func() { _ = response.Body.Close() }()
	response.Close = true
	return response.Write(stream)
}

func (r *Hub) finishOAuthCallbacks(event codex.Event) {
	if event.Method != "mcpServer/oauthLogin/completed" {
		return
	}
	var result struct {
		Name     string
		ThreadID string
	}
	if json.Unmarshal(event.Params, &result) != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for state, flow := range r.oauthCallbacks {
		if flow.name == result.Name && flow.threadID == result.ThreadID {
			delete(r.oauthCallbacks, state)
		}
	}
}
