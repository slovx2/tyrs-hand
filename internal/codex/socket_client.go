package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

type ServerRequest struct {
	ID     json.RawMessage
	Method string
	Params json.RawMessage
}

type ServerRequestHandler func(context.Context, ServerRequest) (any, error)

type SocketClientOptions struct {
	SocketPath           string
	RequestTimeout       time.Duration
	ServerRequestTimeout time.Duration
	EventBacklog         int
	ServerRequestHandler ServerRequestHandler
}

type ThreadFilter struct {
	ThreadID string
	TurnID   string
}

type SocketClient struct {
	options          SocketClientOptions
	ws               *websocket.Conn
	initializeResult json.RawMessage

	writeMu sync.Mutex
	mu      sync.Mutex
	nextID  atomic.Int64
	pending map[string]chan rpcMessage
	subs    map[int64]*EventSubscription
	nextSub int64
	done    chan struct{}
	err     error
}

type EventSubscription struct {
	id     int64
	client *SocketClient
	filter ThreadFilter
	events chan Event
	once   sync.Once
}

func ConnectSocket(ctx context.Context, options SocketClientOptions) (*SocketClient, error) {
	if options.SocketPath == "" {
		return nil, errors.New("连接 Codex App Server 缺少 Unix Socket 路径")
	}
	if options.RequestTimeout <= 0 {
		options.RequestTimeout = 30 * time.Second
	}
	if options.ServerRequestTimeout <= 0 {
		options.ServerRequestTimeout = 60 * time.Second
	}
	if options.EventBacklog <= 0 {
		options.EventBacklog = 4096
	}
	dialer := websocket.Dialer{NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var unix net.Dialer
		return unix.DialContext(ctx, "unix", options.SocketPath)
	}}
	ws, response, err := dialer.DialContext(ctx, "ws://localhost/", http.Header{})
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("连接 Codex App Server Unix Socket: %w", err)
	}
	client := &SocketClient{options: options, ws: ws, pending: make(map[string]chan rpcMessage),
		subs: make(map[int64]*EventSubscription), done: make(chan struct{})}
	go client.readLoop()
	initCtx, cancel := context.WithTimeout(ctx, options.RequestTimeout)
	defer cancel()
	var result json.RawMessage
	if err := client.Call(initCtx, "initialize", map[string]any{
		"clientInfo":   map[string]string{"name": "tyrs-hand", "title": "Tyrs Hand", "version": "0.1.0"},
		"capabilities": map[string]any{"experimentalApi": true},
	}, &result); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("初始化 Codex App Server Socket: %w", err)
	}
	client.initializeResult = append(json.RawMessage(nil), result...)
	if err := client.notify("initialized", map[string]any{}); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}

func (c *SocketClient) InitializeResult() json.RawMessage {
	return append(json.RawMessage(nil), c.initializeResult...)
}

func (c *SocketClient) Call(ctx context.Context, method string, params, result any) error {
	id := c.nextID.Add(1)
	key := strconv.FormatInt(id, 10)
	response := make(chan rpcMessage, 1)
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return &RequestError{Method: method, State: RequestNotSent, Cause: err}
	}
	c.pending[key] = response
	c.mu.Unlock()
	if err := c.write(requestEnvelope{ID: id, Method: method, Params: params}); err != nil {
		c.removePending(key)
		return &RequestError{Method: method, State: RequestUnknown, Cause: err}
	}
	select {
	case message, ok := <-response:
		if !ok {
			return &RequestError{Method: method, State: RequestUnknown, Cause: c.failure()}
		}
		if message.Error != nil {
			return &RequestError{Method: method, State: RequestRejected,
				Cause: message.Error}
		}
		if result != nil && len(message.Result) > 0 {
			if err := json.Unmarshal(message.Result, result); err != nil {
				return &RequestError{Method: method, State: RequestUnknown, Cause: err}
			}
		}
		return nil
	case <-ctx.Done():
		c.removePending(key)
		return &RequestError{Method: method, State: RequestUnknown, Cause: ctx.Err()}
	case <-c.done:
		return &RequestError{Method: method, State: RequestUnknown, Cause: c.failure()}
	}
}

func (c *SocketClient) Notify(method string, params any) error {
	return c.notify(method, params)
}

func (c *SocketClient) Subscribe(filter ThreadFilter) *EventSubscription {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextSub++
	subscription := &EventSubscription{id: c.nextSub, client: c, filter: filter,
		events: make(chan Event, c.options.EventBacklog)}
	c.subs[subscription.id] = subscription
	return subscription
}

func (s *EventSubscription) Events() <-chan Event { return s.events }

func (s *EventSubscription) Close() {
	s.once.Do(func() {
		s.client.mu.Lock()
		if _, ok := s.client.subs[s.id]; ok {
			delete(s.client.subs, s.id)
			close(s.events)
		}
		s.client.mu.Unlock()
	})
}

func (c *SocketClient) Close() error {
	_ = c.ws.Close()
	<-c.done
	err := c.failure()
	if errors.Is(err, net.ErrClosed) || websocket.IsCloseError(err, websocket.CloseNormalClosure) {
		return nil
	}
	return nil
}

func (c *SocketClient) notify(method string, params any) error {
	return c.write(notificationEnvelope{Method: method, Params: params})
}

func (c *SocketClient) write(value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.ws.WriteMessage(websocket.TextMessage, payload)
}

func (c *SocketClient) readLoop() {
	for {
		_, payload, err := c.ws.ReadMessage()
		if err != nil {
			c.fail(err)
			return
		}
		var message rpcMessage
		if err := json.Unmarshal(payload, &message); err != nil {
			c.fail(fmt.Errorf("Codex Socket 返回非法 JSON: %w", err))
			return
		}
		if len(message.ID) > 0 && message.Method != "" {
			go c.handleServerRequest(message)
			continue
		}
		if len(message.ID) > 0 {
			c.deliver(message)
			continue
		}
		if message.Method != "" {
			c.publish(Event{Method: message.Method, Params: message.Params})
		}
	}
}

func (c *SocketClient) handleServerRequest(message rpcMessage) {
	if c.options.ServerRequestHandler == nil {
		_ = c.write(responseEnvelope{ID: message.ID,
			Error: &rpcError{Code: -32601, Message: "unsupported server request"}})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.options.ServerRequestTimeout)
	defer cancel()
	result, err := c.options.ServerRequestHandler(ctx,
		ServerRequest{ID: message.ID, Method: message.Method, Params: message.Params})
	if err != nil {
		_ = c.write(responseEnvelope{ID: message.ID, Error: &rpcError{Code: -32000, Message: err.Error()}})
		return
	}
	_ = c.write(responseEnvelope{ID: message.ID, Result: result})
}

func (c *SocketClient) deliver(message rpcMessage) {
	key := string(message.ID)
	c.mu.Lock()
	response := c.pending[key]
	delete(c.pending, key)
	c.mu.Unlock()
	if response != nil {
		response <- message
	}
}

func (c *SocketClient) publish(event Event) {
	threadID, turnID := eventScope(event.Params)
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, subscription := range c.subs {
		if subscription.filter.ThreadID != "" && subscription.filter.ThreadID != threadID {
			continue
		}
		if subscription.filter.TurnID != "" && subscription.filter.TurnID != turnID {
			continue
		}
		select {
		case subscription.events <- event:
		default:
			c.err = fmt.Errorf("Codex Socket 订阅 backlog 超过 %d 条", cap(subscription.events))
			_ = c.ws.Close()
		}
	}
}

func eventScope(raw json.RawMessage) (string, string) {
	var params struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		Thread   struct {
			ID string `json:"id"`
		} `json:"thread"`
		Turn struct {
			ID       string `json:"id"`
			ThreadID string `json:"threadId"`
		} `json:"turn"`
	}
	_ = json.Unmarshal(raw, &params)
	if params.ThreadID == "" {
		params.ThreadID = params.Thread.ID
	}
	if params.ThreadID == "" {
		params.ThreadID = params.Turn.ThreadID
	}
	if params.TurnID == "" {
		params.TurnID = params.Turn.ID
	}
	return params.ThreadID, params.TurnID
}

func (c *SocketClient) removePending(key string) {
	c.mu.Lock()
	delete(c.pending, key)
	c.mu.Unlock()
}

func (c *SocketClient) failure() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err == nil {
		return ioEOF{}
	}
	return c.err
}

func (c *SocketClient) fail(err error) {
	c.mu.Lock()
	if c.err == nil {
		c.err = err
		for key, response := range c.pending {
			delete(c.pending, key)
			close(response)
		}
		for id, subscription := range c.subs {
			delete(c.subs, id)
			close(subscription.events)
		}
		close(c.done)
	}
	c.mu.Unlock()
}

type ioEOF struct{}

func (ioEOF) Error() string { return "Codex Socket 已关闭" }
