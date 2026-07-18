package codex

import (
	"context"
	"errors"
	"sync"
	"time"

	"go.uber.org/zap"
)

type PoolOptions struct {
	Bin            string
	RequestTimeout time.Duration
	ToolTimeout    time.Duration
	Logger         *zap.Logger
}

type poolEntry struct {
	client   *Client
	handlers map[string]ToolHandler
}

type Pool struct {
	options PoolOptions

	mu      sync.Mutex
	entries map[string]*poolEntry
	closed  bool
}

func NewPool(options PoolOptions) *Pool {
	if options.Logger == nil {
		options.Logger = zap.NewNop()
	}
	return &Pool{options: options, entries: make(map[string]*poolEntry)}
}

func (p *Pool) Acquire(ctx context.Context, key, cwd, codexHome string, environment []string) (*Client, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, errors.New("当前 Codex 进程池已经关闭")
	}
	if entry := p.entries[key]; entry != nil {
		select {
		case <-entry.client.Done():
			delete(p.entries, key)
		default:
			client := entry.client
			p.mu.Unlock()
			return client, nil
		}
	}
	entry := &poolEntry{handlers: make(map[string]ToolHandler)}
	p.mu.Unlock()

	client, err := Start(ctx, ClientOptions{
		Bin: p.options.Bin, CWD: cwd, CodexHome: codexHome, Environment: environment,
		RequestTimeout: p.options.RequestTimeout, ToolTimeout: p.options.ToolTimeout,
		Logger: p.options.Logger,
		ToolHandler: func(toolCtx context.Context, request ToolCallRequest) (ToolCallResult, error) {
			return p.routeTool(key, toolCtx, request)
		},
	})
	if err != nil {
		return nil, err
	}
	entry.client = client
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		_ = client.Close()
		return nil, errors.New("当前 Codex 进程池已经关闭")
	}
	if existing := p.entries[key]; existing != nil {
		p.mu.Unlock()
		_ = client.Close()
		return existing.client, nil
	}
	p.entries[key] = entry
	p.mu.Unlock()
	return client, nil
}

func (p *Pool) Bind(key, threadID string, handler ToolHandler) (func(), error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entry := p.entries[key]
	if entry == nil {
		return nil, errors.New("当前 Codex 进程池条目不存在")
	}
	entry.handlers[threadID] = handler
	return func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if current := p.entries[key]; current == entry {
			delete(current.handlers, threadID)
		}
	}, nil
}

func (p *Pool) routeTool(key string, ctx context.Context, request ToolCallRequest) (ToolCallResult, error) {
	p.mu.Lock()
	entry := p.entries[key]
	var handler ToolHandler
	if entry != nil {
		handler = entry.handlers[request.ThreadID]
	}
	p.mu.Unlock()
	if handler == nil {
		return ToolCallResult{}, errors.New("当前 Thread 没有活动的工具授权")
	}
	return handler(ctx, request)
}

func (p *Pool) Close() error {
	p.mu.Lock()
	p.closed = true
	clients := make([]*Client, 0, len(p.entries))
	for _, entry := range p.entries {
		clients = append(clients, entry.client)
	}
	p.entries = make(map[string]*poolEntry)
	p.mu.Unlock()
	for _, client := range clients {
		_ = client.Close()
	}
	return nil
}
