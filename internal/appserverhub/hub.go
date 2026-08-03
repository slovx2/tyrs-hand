package appserverhub

import (
	"context"
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

type Hub struct {
	options        Options
	upstream       *codex.SocketClient
	upstreamEvents *codex.EventSubscription
	listener       net.Listener
	httpServer     *http.Server

	mu                sync.Mutex
	sessions          map[int64]*session
	ephemeralThreads  map[string]bool
	archiveOperations map[string]*archiveOperation
	nextID            atomic.Int64
	closed            bool
	stats             Stats
	done              chan struct{}
}

func Start(ctx context.Context, options Options) (*Hub, error) {
	if options.UpstreamSocketPath == "" {
		return nil, errors.New("启动 Codex Hub 缺少 App Server Socket")
	}
	if options.RequestTimeout <= 0 {
		options.RequestTimeout = 30 * time.Second
	}
	if options.LifecycleRequestTimeout <= 0 {
		options.LifecycleRequestTimeout = 24 * time.Hour
	}
	if options.ServerRequestTimeout <= 0 {
		options.ServerRequestTimeout = 24 * time.Hour
	}
	if options.EventBacklog <= 0 {
		options.EventBacklog = 4096
	}
	hub := &Hub{options: options, sessions: make(map[int64]*session),
		ephemeralThreads:  make(map[string]bool),
		archiveOperations: make(map[string]*archiveOperation), done: make(chan struct{})}
	upstream, err := codex.ConnectSocket(ctx, codex.SocketClientOptions{
		SocketPath: options.UpstreamSocketPath, RequestTimeout: options.RequestTimeout,
		ServerRequestTimeout: options.ServerRequestTimeout, EventBacklog: options.EventBacklog,
		ServerRequestHandler: hub.handleServerRequest,
	})
	if err != nil {
		return nil, err
	}
	hub.upstream = upstream
	hub.upstreamEvents = upstream.Subscribe(codex.ThreadFilter{})
	hub.stats.UpstreamConnections = 1
	hub.stats.UpstreamInitializations = 1
	if options.SocketPath != "" {
		if err := hub.listen(); err != nil {
			hub.upstreamEvents.Close()
			_ = upstream.Close()
			return nil, err
		}
	}
	go hub.forwardEvents()
	go func() {
		select {
		case <-ctx.Done():
			_ = hub.Close()
		case <-hub.done:
		}
	}()
	return hub, nil
}

func (r *Hub) listen() error {
	if err := os.MkdirAll(filepath.Dir(r.options.SocketPath), 0o770); err != nil {
		return fmt.Errorf("创建 Hub Socket 目录: %w", err)
	}
	_ = os.Remove(r.options.SocketPath)
	listener, err := net.Listen("unix", r.options.SocketPath)
	if err != nil {
		return fmt.Errorf("监听 Hub Socket: %w", err)
	}
	// Hub 与 Codex 都由宿主 Worker 用户运行，Socket 不向其他宿主用户开放。
	if err := os.Chmod(r.options.SocketPath, 0o600); err != nil {
		_ = listener.Close()
		return fmt.Errorf("设置 Hub Socket 权限: %w", err)
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

func (r *Hub) SocketPath() string { return r.options.SocketPath }

func (r *Hub) Stats() Stats {
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

func (r *Hub) Close() error {
	r.shutdown(nil)
	return nil
}

func (r *Hub) shutdown(_ error) {
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

func (r *Hub) addSession(role Role, send func(rpcMessage) error,
	handler codex.ServerRequestHandler,
) (*session, error) {
	if role != RoleDesktop && role != RoleWorker {
		return nil, errors.New("hub 下游角色无效")
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

func (r *Hub) removeSession(s *session) {
	r.mu.Lock()
	if r.sessions[s.id] == s {
		delete(r.sessions, s.id)
	}
	r.mu.Unlock()
	s.close(errSessionClosed)
}
