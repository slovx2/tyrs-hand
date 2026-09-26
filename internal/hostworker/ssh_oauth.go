package hostworker

import (
	"context"
	"io"

	"golang.org/x/crypto/ssh"
)

type oauthCallbackRuntime interface {
	OAuthCallbackAllowed(string, uint32) bool
	ServeOAuthCallback(context.Context, string, uint32, io.ReadWriteCloser) error
}

// SSH 只为当前引擎已登记的 OAuth flow 开放 HTTP 回调，不提供通用端口转发。
func (s *SSHServer) handleOAuthForward(ctx context.Context, request ssh.NewChannel) {
	var destination struct {
		Host       string
		Port       uint32
		OriginHost string
		OriginPort uint32
	}
	runtime, ok := s.options.Runtime.(oauthCallbackRuntime)
	if !ok || ssh.Unmarshal(request.ExtraData(), &destination) != nil || !runtime.OAuthCallbackAllowed(destination.Host, destination.Port) {
		_ = request.Reject(ssh.Prohibited, "Worker 仅允许有效 OAuth 回调")
		return
	}
	channel, requests, err := request.Accept()
	if err != nil {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() { _ = channel.Close() }()
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		go func() {
			ssh.DiscardRequests(requests)
			// requests 在通道完整关闭时结束；写侧 EOF 不应取消合法 HTTP 回调。
			cancel()
		}()
		_ = runtime.ServeOAuthCallback(ctx, destination.Host, destination.Port, channel)
	}()
}
