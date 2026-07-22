package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

type ToolHandler func(context.Context, ToolCallRequest) (ToolCallResult, error)

var errClientClosed = errors.New("当前 Codex App Server 客户端已关闭")

type RequestState string

const (
	RequestNotSent  RequestState = "not_sent"
	RequestRejected RequestState = "rejected"
	RequestUnknown  RequestState = "unknown"
)

type RequestError struct {
	Method string
	State  RequestState
	Cause  error
}

func (e *RequestError) Error() string {
	return fmt.Sprintf("调用 Codex %s（%s）: %v", e.Method, e.State, e.Cause)
}
func (e *RequestError) Unwrap() error { return e.Cause }

type ClientOptions struct {
	Bin            string
	CWD            string
	CodexHome      string
	Home           string
	Environment    []string
	RequestTimeout time.Duration
	ToolTimeout    time.Duration
	CloseTimeout   time.Duration
	MaxFrameBytes  int
	EventBacklog   int
	ToolHandler    ToolHandler
	Launcher       Launcher
	SkipLocalHome  bool
	Logger         *zap.Logger
}

type Client struct {
	options ClientOptions
	process Process
	stdin   io.WriteCloser

	writeMu  sync.Mutex
	mu       sync.Mutex
	pending  map[int64]chan rpcMessage
	nextID   atomic.Int64
	closing  atomic.Bool
	events   chan Event
	done     chan struct{}
	readDone chan struct{}
	err      error
	readErr  error
}

func Start(ctx context.Context, options ClientOptions) (*Client, error) {
	if options.Bin == "" || options.CWD == "" || options.CodexHome == "" {
		return nil, errors.New("启动 Codex 缺少 bin、cwd 或 CODEX_HOME")
	}
	applyClientDefaults(&options)
	if !options.SkipLocalHome {
		if err := os.MkdirAll(options.CodexHome, 0o700); err != nil {
			return nil, err
		}
	}
	home := options.Home
	if home == "" {
		home = options.CodexHome
	}
	process, err := options.Launcher.Launch(ProcessSpec{
		Bin: options.Bin, Args: []string{"app-server", "--listen", "stdio://"},
		Dir: options.CWD, Env: append(cleanEnvironment(options.Environment),
			"CODEX_HOME="+options.CodexHome, "HOME="+home, "RUST_LOG=warn"),
	})
	if err != nil {
		return nil, fmt.Errorf("启动 Codex App Server: %w", err)
	}
	client := &Client{
		options: options, process: process, stdin: process.Stdin(),
		pending: make(map[int64]chan rpcMessage), events: make(chan Event, options.EventBacklog),
		done: make(chan struct{}), readDone: make(chan struct{}),
	}
	go client.readLoop(process.Stdout())
	go client.stderrLoop(process.Stderr())
	go client.waitLoop()

	initCtx, cancel := context.WithTimeout(ctx, options.RequestTimeout)
	defer cancel()
	var result json.RawMessage
	err = client.call(initCtx, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "tyrs-hand", "title": "tyrs-hand", "version": "0.1.0"},
		"capabilities": map[string]any{"experimentalApi": true},
	}, &result)
	if err == nil {
		err = client.notify("initialized", map[string]any{})
	}
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("初始化 Codex App Server: %w", err)
	}
	return client, nil
}

func applyClientDefaults(options *ClientOptions) {
	if options.RequestTimeout <= 0 {
		options.RequestTimeout = 30 * time.Second
	}
	if options.ToolTimeout <= 0 {
		options.ToolTimeout = 60 * time.Second
	}
	if options.CloseTimeout <= 0 {
		options.CloseTimeout = 5 * time.Second
	}
	if options.MaxFrameBytes <= 0 {
		options.MaxFrameBytes = 16 << 20
	}
	if options.EventBacklog <= 0 {
		options.EventBacklog = 4096
	}
	if options.Launcher == nil {
		options.Launcher = ExecLauncher{}
	}
	if options.Logger == nil {
		options.Logger = zap.NewNop()
	}
}

func (c *Client) Events() <-chan Event  { return c.events }
func (c *Client) Done() <-chan struct{} { return c.done }

func (c *Client) Call(ctx context.Context, method string, params, result any) error {
	return c.call(ctx, method, params, result)
}

func (c *Client) Close() error {
	if c.closing.Swap(true) {
		<-c.done
		return normalizeCloseError(c.processError())
	}
	_ = c.stdin.Close()
	_ = c.process.Signal(os.Interrupt)
	select {
	case <-c.done:
	case <-time.After(c.options.CloseTimeout):
		_ = c.process.Kill()
		<-c.done
	}
	return normalizeCloseError(c.processError())
}

func normalizeCloseError(err error) error {
	if errors.Is(err, errClientClosed) || errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

func (c *Client) call(ctx context.Context, method string, params, result any) error {
	id := c.nextID.Add(1)
	responseCh := make(chan rpcMessage, 1)
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return &RequestError{Method: method, State: RequestNotSent, Cause: err}
	}
	c.pending[id] = responseCh
	c.mu.Unlock()
	if err := c.write(requestEnvelope{ID: id, Method: method, Params: params}); err != nil {
		c.removePending(id)
		return &RequestError{Method: method, State: writeRequestState(err), Cause: err}
	}
	select {
	case response, ok := <-responseCh:
		if !ok {
			return &RequestError{Method: method, State: RequestUnknown, Cause: c.processError()}
		}
		if response.Error != nil {
			return &RequestError{Method: method, State: RequestRejected,
				Cause: response.Error}
		}
		if result != nil && len(response.Result) > 0 {
			if err := json.Unmarshal(response.Result, result); err != nil {
				return &RequestError{Method: method, State: RequestUnknown, Cause: err}
			}
		}
		return nil
	case <-ctx.Done():
		c.removePending(id)
		return &RequestError{Method: method, State: RequestUnknown, Cause: ctx.Err()}
	case <-c.done:
		return &RequestError{Method: method, State: RequestUnknown, Cause: c.processError()}
	}
}

func (c *Client) notify(method string, params any) error {
	return c.write(notificationEnvelope{Method: method, Params: params})
}

func (c *Client) removePending(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *Client) processError() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err == nil {
		return errors.New("当前 Codex App Server 连接已经关闭")
	}
	return c.err
}

func absolute(path string) string {
	value, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return value
}
