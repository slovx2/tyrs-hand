package codexrelay

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/slovx2/tyrs-hand/internal/codex"
)

type Client struct {
	relay   *Relay
	session *session
	backlog int
	events  chan codex.Event
	done    chan struct{}

	mu      sync.Mutex
	subs    map[int64]*Subscription
	nextSub atomic.Int64
	global  atomic.Bool
	err     error
	closed  bool
}

type Subscription struct {
	id     int64
	client *Client
	filter codex.ThreadFilter
	events chan codex.Event
	once   sync.Once
}

func (r *Relay) OpenClient(options ClientOptions) (*Client, error) {
	if options.Role == "" {
		options.Role = RoleWorker
	}
	if options.EventBacklog <= 0 {
		options.EventBacklog = r.options.EventBacklog
	}
	client := &Client{relay: r, backlog: options.EventBacklog,
		events: make(chan codex.Event, options.EventBacklog), done: make(chan struct{}),
		subs: make(map[int64]*Subscription)}
	s, err := r.addSession(options.Role, nil, options.ServerRequestHandler)
	if err != nil {
		return nil, err
	}
	client.session = s
	s.client = client
	return client, nil
}

func (c *Client) Call(ctx context.Context, method string, params, result any) error {
	raw, err := marshalRaw(params)
	if err != nil {
		return err
	}
	response, err := c.relay.routeCall(ctx, c.session, method, raw)
	if err != nil {
		return requestError(method, err)
	}
	return decodeResult(response, result)
}

func (c *Client) StartThread(ctx context.Context, params json.RawMessage) (string, error) {
	var response struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := c.Call(ctx, "thread/start", params, &response); err != nil {
		return "", err
	}
	if response.Thread.ID == "" {
		return "", errors.New("thread/start 没有返回 Thread ID")
	}
	return response.Thread.ID, nil
}

func (c *Client) Events() <-chan codex.Event {
	c.global.Store(true)
	return c.events
}

func (c *Client) Done() <-chan struct{} { return c.done }

func (c *Client) Subscribe(filter codex.ThreadFilter) *Subscription {
	subscription := &Subscription{id: c.nextSub.Add(1), client: c, filter: filter,
		events: make(chan codex.Event, c.backlog)}
	c.mu.Lock()
	if c.closed {
		close(subscription.events)
	} else {
		c.subs[subscription.id] = subscription
	}
	c.mu.Unlock()
	return subscription
}

func (s *Subscription) Events() <-chan codex.Event { return s.events }

func (s *Subscription) Close() {
	s.once.Do(func() {
		s.client.mu.Lock()
		if current := s.client.subs[s.id]; current == s {
			delete(s.client.subs, s.id)
			close(s.events)
		}
		s.client.mu.Unlock()
	})
}

func (c *Client) Close() error {
	c.relay.removeSession(c.session)
	<-c.done
	return nil
}

func (c *Client) publish(event codex.Event) error {
	threadID, turnID := threadScope(event.Params)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errSessionClosed
	}
	if c.global.Load() {
		select {
		case c.events <- event:
		default:
			return errors.New("relay worker 全局事件 backlog 已满")
		}
	}
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
			delete(c.subs, subscription.id)
			close(subscription.events)
		}
	}
	return nil
}

func (c *Client) fail(cause error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.err = cause
	for id, subscription := range c.subs {
		delete(c.subs, id)
		close(subscription.events)
	}
	close(c.events)
	close(c.done)
	c.mu.Unlock()
}
