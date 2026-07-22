package codexrelay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/slovx2/tyrs-hand/internal/codex"
)

type session struct {
	id      int64
	role    Role
	send    func(rpcMessage) error
	handler codex.ServerRequestHandler

	mu            sync.Mutex
	subscriptions map[string]bool
	pending       map[string]chan serverOutcome
	closed        bool
	closeErr      error
	client        *Client
}

func newSession(id int64, role Role, send func(rpcMessage) error,
	handler codex.ServerRequestHandler,
) *session {
	return &session{id: id, role: role, send: send, handler: handler,
		subscriptions: make(map[string]bool), pending: make(map[string]chan serverOutcome)}
}

func (s *session) subscribe(threadID string) {
	if threadID == "" {
		return
	}
	s.mu.Lock()
	if !s.closed {
		s.subscriptions[threadID] = true
	}
	s.mu.Unlock()
}

func (s *session) unsubscribe(threadID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	wasSubscribed := s.subscriptions[threadID]
	delete(s.subscriptions, threadID)
	return wasSubscribed
}

func (s *session) subscribed(threadID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.subscriptions[threadID]
}

func (s *session) publish(event codex.Event) error {
	if s.client != nil {
		return s.client.publish(event)
	}
	return s.write(rpcMessage{Method: event.Method, Params: event.Params})
}

func (s *session) write(message rpcMessage) error {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed || s.send == nil {
		return errSessionClosed
	}
	return s.send(message)
}

func (s *session) invoke(ctx context.Context, request codex.ServerRequest) (json.RawMessage, error) {
	if s.handler != nil {
		value, err := s.handler(ctx, request)
		if err != nil {
			return nil, err
		}
		return marshalRaw(value)
	}
	if s.role != RoleDesktop {
		return nil, errors.New("worker client 没有 server request handler")
	}
	key := string(request.ID)
	result := make(chan serverOutcome, 1)
	s.mu.Lock()
	if s.closed {
		err := s.closeErr
		s.mu.Unlock()
		return nil, err
	}
	if _, exists := s.pending[key]; exists {
		s.mu.Unlock()
		return nil, errors.New("desktop server request ID 重复")
	}
	s.pending[key] = result
	s.mu.Unlock()
	if err := s.write(rpcMessage{ID: request.ID, Method: request.Method, Params: request.Params}); err != nil {
		s.takePending(key)
		return nil, err
	}
	select {
	case outcome, ok := <-result:
		if !ok {
			return nil, errSessionClosed
		}
		return outcome.result, outcome.err
	case <-ctx.Done():
		s.takePending(key)
		return nil, ctx.Err()
	}
}

func (s *session) resolve(message rpcMessage) bool {
	result := s.takePending(string(message.ID))
	if result == nil {
		return false
	}
	if message.Error != nil {
		result <- serverOutcome{err: fmt.Errorf("%d %s", message.Error.Code, message.Error.Message)}
	} else {
		result <- serverOutcome{result: append(json.RawMessage(nil), message.Result...)}
	}
	return true
}

func (s *session) takePending(key string) chan serverOutcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := s.pending[key]
	delete(s.pending, key)
	return result
}

func (s *session) close(cause error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.closeErr = cause
	pending := s.pending
	s.pending = make(map[string]chan serverOutcome)
	client := s.client
	s.mu.Unlock()
	for _, result := range pending {
		close(result)
	}
	if client != nil {
		client.fail(cause)
	}
}

func threadScope(raw json.RawMessage) (string, string) {
	var value struct {
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
	_ = json.Unmarshal(raw, &value)
	if value.ThreadID == "" {
		value.ThreadID = value.Thread.ID
	}
	if value.ThreadID == "" {
		value.ThreadID = value.Turn.ThreadID
	}
	if value.TurnID == "" {
		value.TurnID = value.Turn.ID
	}
	return value.ThreadID, value.TurnID
}
