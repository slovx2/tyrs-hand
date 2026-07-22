package codexrelay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
)

type Relay struct {
	options        Options
	upstream       *codex.SocketClient
	upstreamEvents *codex.EventSubscription
	listener       net.Listener
	httpServer     *http.Server

	mu       sync.Mutex
	sessions map[int64]*session
	threads  map[string]json.RawMessage
	nextID   atomic.Int64
	closed   bool
	stats    Stats
	done     chan struct{}
}

func Start(ctx context.Context, options Options) (*Relay, error) {
	if options.SocketPath == "" || options.UpstreamSocketPath == "" {
		return nil, errors.New("启动 Codex Relay 缺少下游或上游 Socket")
	}
	if options.RequestTimeout <= 0 {
		options.RequestTimeout = 30 * time.Second
	}
	if options.ServerRequestTimeout <= 0 {
		options.ServerRequestTimeout = 24 * time.Hour
	}
	if options.EventBacklog <= 0 {
		options.EventBacklog = 4096
	}
	relay := &Relay{options: options, sessions: make(map[int64]*session),
		threads: make(map[string]json.RawMessage), done: make(chan struct{})}
	upstream, err := codex.ConnectSocket(ctx, codex.SocketClientOptions{
		SocketPath: options.UpstreamSocketPath, RequestTimeout: options.RequestTimeout,
		ServerRequestTimeout: options.ServerRequestTimeout, EventBacklog: options.EventBacklog,
		ServerRequestHandler: relay.handleServerRequest,
	})
	if err != nil {
		return nil, err
	}
	relay.upstream = upstream
	relay.upstreamEvents = upstream.Subscribe(codex.ThreadFilter{})
	relay.stats.UpstreamConnections = 1
	relay.stats.UpstreamInitializations = 1
	if err := relay.listen(); err != nil {
		relay.upstreamEvents.Close()
		_ = upstream.Close()
		return nil, err
	}
	go relay.forwardEvents()
	go func() {
		select {
		case <-ctx.Done():
			_ = relay.Close()
		case <-relay.done:
		}
	}()
	return relay, nil
}

func (r *Relay) listen() error {
	if err := os.MkdirAll(filepath.Dir(r.options.SocketPath), 0o770); err != nil {
		return fmt.Errorf("创建 Relay Socket 目录: %w", err)
	}
	_ = os.Remove(r.options.SocketPath)
	listener, err := net.Listen("unix", r.options.SocketPath)
	if err != nil {
		return fmt.Errorf("监听 Relay Socket: %w", err)
	}
	// Relay 与开发容器运行用户处于不同的 UID/GID 命名空间，宿主父目录负责隔离环境。
	if err := os.Chmod(r.options.SocketPath, 0o666); err != nil {
		_ = listener.Close()
		return fmt.Errorf("设置 Relay Socket 权限: %w", err)
	}
	r.listener = listener
	r.httpServer = &http.Server{Handler: http.HandlerFunc(r.serveDesktop)}
	go func() {
		if err := r.httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			r.shutdown(err)
		}
	}()
	return nil
}

func (r *Relay) SocketPath() string { return r.options.SocketPath }

func (r *Relay) Stats() Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := r.stats
	result.DesktopConnections = 0
	result.WorkerConnections = 0
	for _, item := range r.sessions {
		if item.role == RoleWorker {
			result.WorkerConnections++
		} else {
			result.DesktopConnections++
		}
	}
	return result
}

func (r *Relay) Close() error {
	r.shutdown(nil)
	return nil
}

func (r *Relay) shutdown(_ error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	sessions := make([]*session, 0, len(r.sessions))
	for _, item := range r.sessions {
		sessions = append(sessions, item)
	}
	r.sessions = make(map[int64]*session)
	close(r.done)
	r.mu.Unlock()
	for _, item := range sessions {
		item.close(errSessionClosed)
	}
	if r.httpServer != nil {
		_ = r.httpServer.Close()
	}
	if r.upstreamEvents != nil {
		r.upstreamEvents.Close()
	}
	if r.upstream != nil {
		_ = r.upstream.Close()
	}
	_ = os.Remove(r.options.SocketPath)
}

func (r *Relay) addSession(role Role, send func(rpcMessage) error,
	handler codex.ServerRequestHandler,
) (*session, error) {
	if role != RoleDesktop && role != RoleWorker {
		return nil, errors.New("relay 下游角色无效")
	}
	s := newSession(r.nextID.Add(1), role, send, handler)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, errSessionClosed
	}
	r.sessions[s.id] = s
	return s, nil
}

func (r *Relay) removeSession(s *session) {
	r.mu.Lock()
	if r.sessions[s.id] == s {
		delete(r.sessions, s.id)
	}
	r.mu.Unlock()
	s.close(errSessionClosed)
}
