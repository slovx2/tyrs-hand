package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

type ToolHandler func(context.Context, ToolCallRequest) (ToolCallResult, error)

var errClientClosed = errors.New("当前 Codex App Server 客户端已关闭")

type ClientOptions struct {
	Bin            string
	CWD            string
	CodexHome      string
	Environment    []string
	RequestTimeout time.Duration
	ToolTimeout    time.Duration
	ToolHandler    ToolHandler
	Logger         *zap.Logger
}

type Client struct {
	options ClientOptions
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	cancel  context.CancelFunc

	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[int64]chan rpcMessage
	nextID  atomic.Int64
	closing atomic.Bool
	events  chan Event
	done    chan struct{}
	err     error
}

func Start(ctx context.Context, options ClientOptions) (*Client, error) {
	if options.Bin == "" || options.CWD == "" || options.CodexHome == "" {
		return nil, errors.New("启动 Codex 缺少 bin、cwd 或 CODEX_HOME")
	}
	if options.RequestTimeout <= 0 {
		options.RequestTimeout = 30 * time.Second
	}
	if options.ToolTimeout <= 0 {
		options.ToolTimeout = 60 * time.Second
	}
	if options.Logger == nil {
		options.Logger = zap.NewNop()
	}
	if err := os.MkdirAll(options.CodexHome, 0o700); err != nil {
		return nil, err
	}
	processCtx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(processCtx, options.Bin, "app-server", "--listen", "stdio://")
	cmd.Dir = options.CWD
	cmd.Env = append(cleanEnvironment(options.Environment),
		"CODEX_HOME="+options.CodexHome,
		"HOME="+options.CodexHome,
		"RUST_LOG=warn",
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	client := &Client{
		options: options, cmd: cmd, stdin: stdin, cancel: cancel,
		pending: make(map[int64]chan rpcMessage), events: make(chan Event, 256), done: make(chan struct{}),
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("启动 Codex App Server: %w", err)
	}
	go client.readLoop(stdout)
	go client.stderrLoop(stderr)
	go client.waitLoop()

	initCtx, initCancel := context.WithTimeout(ctx, options.RequestTimeout)
	defer initCancel()
	var result json.RawMessage
	err = client.call(initCtx, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "tyrs-hand", "title": "tyrs-hand", "version": "0.1.0"},
		"capabilities": map[string]any{"experimentalApi": true},
	}, &result)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("初始化 Codex App Server: %w", err)
	}
	if err := client.notify("initialized", map[string]any{}); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}

func (c *Client) Events() <-chan Event { return c.events }

func (c *Client) Done() <-chan struct{} { return c.done }

func (c *Client) Call(ctx context.Context, method string, params any, result any) error {
	return c.call(ctx, method, params, result)
}

func (c *Client) Close() error {
	c.closing.Store(true)
	c.cancel()
	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		<-c.done
	}
	if errors.Is(c.err, errClientClosed) {
		return nil
	}
	return c.err
}

func (c *Client) call(ctx context.Context, method string, params any, result any) error {
	id := c.nextID.Add(1)
	responseCh := make(chan rpcMessage, 1)
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return err
	}
	c.pending[id] = responseCh
	c.mu.Unlock()
	if err := c.write(requestEnvelope{ID: id, Method: method, Params: params}); err != nil {
		c.removePending(id)
		return err
	}
	select {
	case response, ok := <-responseCh:
		if !ok {
			return c.processError()
		}
		if response.Error != nil {
			return fmt.Errorf("调用 Codex %s: %d %s", method, response.Error.Code, response.Error.Message)
		}
		if result != nil && len(response.Result) > 0 {
			if err := json.Unmarshal(response.Result, result); err != nil {
				return fmt.Errorf("解析 Codex %s 响应: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		c.removePending(id)
		return ctx.Err()
	case <-c.done:
		return c.processError()
	}
}

func (c *Client) notify(method string, params any) error {
	return c.write(notificationEnvelope{Method: method, Params: params})
}

func (c *Client) write(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.stdin.Write(append(data, '\n'))
	return err
}

func (c *Client) readLoop(reader io.Reader) {
	// readLoop 是事件队列唯一的写入者，也由它关闭队列，避免进程退出与尾部事件并发时向已关闭队列发送。
	defer close(c.events)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var message rpcMessage
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			c.options.Logger.Warn("忽略无效 Codex 消息", zap.Error(err))
			continue
		}
		if len(message.ID) > 0 && message.Method != "" {
			go c.handleServerRequest(message)
			continue
		}
		if len(message.ID) > 0 {
			c.deliverResponse(message)
			continue
		}
		if message.Method != "" {
			select {
			case c.events <- Event{Method: message.Method, Params: message.Params}:
			default:
				c.options.Logger.Warn("Codex 事件队列已满", zap.String("method", message.Method))
			}
		}
	}
	if err := scanner.Err(); err != nil && !c.closing.Load() && !errors.Is(err, os.ErrClosed) {
		c.fail(fmt.Errorf("读取 Codex 输出: %w", err))
	}
}

func (c *Client) handleServerRequest(message rpcMessage) {
	if message.Method != "item/tool/call" || c.options.ToolHandler == nil {
		_ = c.write(responseEnvelope{ID: message.ID, Error: &rpcError{Code: -32601, Message: "unsupported server request"}})
		return
	}
	var request ToolCallRequest
	if err := json.Unmarshal(message.Params, &request); err != nil {
		_ = c.write(responseEnvelope{ID: message.ID, Error: &rpcError{Code: -32602, Message: "invalid tool call"}})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.options.ToolTimeout)
	defer cancel()
	result, err := c.options.ToolHandler(ctx, request)
	if err != nil {
		result = TextToolResult(err.Error(), false)
	}
	_ = c.write(responseEnvelope{ID: message.ID, Result: result})
}

func (c *Client) deliverResponse(message rpcMessage) {
	id, err := strconv.ParseInt(string(message.ID), 10, 64)
	if err != nil {
		return
	}
	c.mu.Lock()
	ch := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if ch != nil {
		ch <- message
	}
}

func (c *Client) removePending(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *Client) waitLoop() {
	err := c.cmd.Wait()
	if c.closing.Load() {
		c.fail(errClientClosed)
	} else if err != nil && !errors.Is(err, context.Canceled) {
		c.fail(fmt.Errorf("当前 Codex App Server 退出: %w", err))
	} else {
		c.fail(io.EOF)
	}
}

func (c *Client) fail(err error) {
	c.mu.Lock()
	if c.err == nil {
		c.err = err
		for id, ch := range c.pending {
			delete(c.pending, id)
			close(ch)
		}
		close(c.done)
	}
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

func (c *Client) stderrLoop(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		c.options.Logger.Debug("Codex", zap.String("line", scanner.Text()))
	}
}

func cleanEnvironment(extra []string) []string {
	allowed := map[string]bool{"PATH": true, "LANG": true, "LC_ALL": true, "HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true, "NO_PROXY": true, "SSL_CERT_FILE": true, "CODEX_CA_CERTIFICATE": true}
	result := make([]string, 0, len(os.Environ())+len(extra))
	for _, item := range os.Environ() {
		key, _, _ := stringsCut(item, "=")
		if allowed[key] {
			result = append(result, item)
		}
	}
	return append(result, extra...)
}

func stringsCut(value, separator string) (string, string, bool) {
	for i := 0; i+len(separator) <= len(value); i++ {
		if value[i:i+len(separator)] == separator {
			return value[:i], value[i+len(separator):], true
		}
	}
	return value, "", false
}

func absolute(path string) string {
	value, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return value
}
