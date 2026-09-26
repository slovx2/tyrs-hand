package hostworker

import (
	"context"
	"errors"
	"io"
)

func (r *Runtime) OAuthCallbackAllowed(host string, port uint32) bool {
	r.mu.Lock()
	current := r.current
	closed := r.closed
	r.mu.Unlock()
	return !closed && current != nil && current.hub.OAuthCallbackAllowed(host, port)
}

func (r *Runtime) ServeOAuthCallback(ctx context.Context, host string, port uint32, stream io.ReadWriteCloser) error {
	r.mu.Lock()
	current := r.current
	closed := r.closed
	r.mu.Unlock()
	if closed || current == nil {
		return errors.New("OAuth 运行时不可用")
	}
	return current.hub.ServeOAuthCallback(ctx, host, port, stream)
}
