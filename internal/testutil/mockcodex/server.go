package mockcodex

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/gorilla/websocket"
)

type Message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *RPCError       `json:"error,omitempty"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type Thread struct {
	ID     string `json:"id"`
	CWD    string `json:"cwd"`
	Status any    `json:"status"`
	Turns  []Turn `json:"turns,omitempty"`
}

type Turn struct {
	ID                  string `json:"id"`
	ThreadID            string `json:"threadId"`
	Status              string `json:"status"`
	ClientUserMessageID string `json:"clientUserMessageId,omitempty"`
}

type Request struct {
	ConnectionID int64
	Message      Message
}

type Server struct {
	SocketPath string

	listener net.Listener
	http     *http.Server
	nextID   atomic.Int64

	mu          sync.Mutex
	connections map[int64]*connection
	threads     map[string]Thread
	pending     map[string]pendingRequest
	requests    chan Request
}

type connection struct {
	id            int64
	server        *Server
	ws            *websocket.Conn
	writeMu       sync.Mutex
	initialized   bool
	subscriptions map[string]bool
}

type pendingRequest struct {
	threadID  string
	resolved  bool
	result    json.RawMessage
	responses int
}

func Start(t interface{ Cleanup(func()) }) (*Server, error) {
	directory, err := os.MkdirTemp("", "tyrs-mock-codex-*")
	if err != nil {
		return nil, err
	}
	socketPath := filepath.Join(directory, "app-server.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		_ = os.RemoveAll(directory)
		return nil, err
	}
	server := &Server{SocketPath: socketPath, listener: listener,
		connections: make(map[int64]*connection), threads: make(map[string]Thread),
		pending: make(map[string]pendingRequest), requests: make(chan Request, 256)}
	server.http = &http.Server{Handler: http.HandlerFunc(server.serve)}
	go func() { _ = server.http.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = os.RemoveAll(directory)
	})
	return server, nil
}

func (s *Server) Requests() <-chan Request { return s.requests }

func (s *Server) Close() error {
	s.mu.Lock()
	connections := make([]*connection, 0, len(s.connections))
	for _, item := range s.connections {
		connections = append(connections, item)
	}
	s.connections = make(map[int64]*connection)
	s.mu.Unlock()
	for _, item := range connections {
		_ = item.ws.Close()
	}
	return s.http.Close()
}

func (s *Server) DisconnectAll() {
	s.mu.Lock()
	connections := make([]*connection, 0, len(s.connections))
	for _, item := range s.connections {
		connections = append(connections, item)
	}
	s.mu.Unlock()
	for _, item := range connections {
		_ = item.ws.Close()
	}
}

func (s *Server) serve(response http.ResponseWriter, request *http.Request) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	ws, err := upgrader.Upgrade(response, request, nil)
	if err != nil {
		return
	}
	id := s.nextID.Add(1)
	conn := &connection{id: id, server: s, ws: ws, subscriptions: make(map[string]bool)}
	s.mu.Lock()
	s.connections[id] = conn
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.connections, id)
		s.mu.Unlock()
		_ = ws.Close()
	}()
	conn.readLoop()
}

func (c *connection) readLoop() {
	for {
		_, payload, err := c.ws.ReadMessage()
		if err != nil {
			return
		}
		var message Message
		if json.Unmarshal(payload, &message) != nil {
			return
		}
		if len(message.ID) > 0 && message.Method == "" {
			c.server.resolve(message)
			continue
		}
		select {
		case c.server.requests <- Request{ConnectionID: c.id, Message: message}:
		default:
		}
		c.handle(message)
	}
}

func (c *connection) handle(message Message) {
	if message.Method == "initialized" {
		return
	}
	if !c.initialized && message.Method != "initialize" {
		c.respondError(message.ID, -32002, "Not initialized")
		return
	}
	switch message.Method {
	case "initialize":
		c.initialized = true
		c.respond(message.ID, map[string]any{"codexHome": "/mock/codex", "platformFamily": "unix", "platformOs": "linux"})
	case "account/read":
		c.respond(message.ID, map[string]any{
			"account": map[string]any{"type": "apiKey"}, "requiresOpenaiAuth": true,
		})
	case "thread/start":
		var params struct {
			CWD string `json:"cwd"`
		}
		_ = json.Unmarshal(message.Params, &params)
		thread := c.server.createThread(params.CWD)
		c.subscriptions[thread.ID] = true
		c.respond(message.ID, threadResponse(thread))
		c.server.broadcast(thread.ID, "thread/started", map[string]any{"thread": thread})
	case "thread/fork":
		var params struct {
			ThreadID string `json:"threadId"`
		}
		_ = json.Unmarshal(message.Params, &params)
		source, ok := c.server.thread(params.ThreadID)
		if !ok {
			c.respondError(message.ID, -32602, "unknown source thread")
			return
		}
		thread := c.server.createThread(source.CWD)
		c.subscriptions[thread.ID] = true
		c.respond(message.ID, threadResponse(thread))
		c.server.broadcast(thread.ID, "thread/started", map[string]any{"thread": thread})
	case "thread/resume":
		var params struct {
			ThreadID string `json:"threadId"`
		}
		_ = json.Unmarshal(message.Params, &params)
		thread, ok := c.server.thread(params.ThreadID)
		if !ok {
			c.respondError(message.ID, -32602, "unknown thread")
			return
		}
		c.subscriptions[thread.ID] = true
		c.respond(message.ID, threadResponse(thread))
	case "thread/read":
		var params struct {
			ThreadID string `json:"threadId"`
		}
		_ = json.Unmarshal(message.Params, &params)
		thread, ok := c.server.thread(params.ThreadID)
		if !ok {
			c.respondError(message.ID, -32602, "unknown thread")
			return
		}
		c.respond(message.ID, map[string]any{"thread": thread})
	case "thread/list":
		c.respond(message.ID, map[string]any{"data": c.server.listThreads(), "nextCursor": nil})
	case "thread/unsubscribe":
		var params struct {
			ThreadID string `json:"threadId"`
		}
		_ = json.Unmarshal(message.Params, &params)
		delete(c.subscriptions, params.ThreadID)
		c.respond(message.ID, map[string]string{"status": "unsubscribed"})
	case "turn/start":
		var params struct {
			ThreadID            string `json:"threadId"`
			ClientUserMessageID string `json:"clientUserMessageId"`
		}
		_ = json.Unmarshal(message.Params, &params)
		turn, err := c.server.startTurn(params.ThreadID, params.ClientUserMessageID)
		if err != nil {
			c.respondError(message.ID, -32000, err.Error())
			return
		}
		c.respond(message.ID, map[string]any{"turn": turn})
		c.server.broadcast(params.ThreadID, "turn/started", map[string]any{
			"threadId": params.ThreadID, "turn": turn,
		})
	case "turn/steer":
		var params struct {
			ThreadID       string `json:"threadId"`
			ExpectedTurnID string `json:"expectedTurnId"`
		}
		_ = json.Unmarshal(message.Params, &params)
		if !c.server.activeTurn(params.ThreadID, params.ExpectedTurnID) {
			c.respondError(message.ID, -32000, "turn is not active")
			return
		}
		c.respond(message.ID, map[string]any{"turnId": params.ExpectedTurnID})
		c.server.broadcast(params.ThreadID, "item/started", map[string]any{
			"threadId": params.ThreadID, "turnId": params.ExpectedTurnID,
			"item": map[string]any{"id": "steer-item", "type": "userMessage"},
		})
	case "turn/interrupt":
		var params struct {
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
		}
		_ = json.Unmarshal(message.Params, &params)
		turn, ok := c.server.finishTurn(params.ThreadID, params.TurnID, "interrupted")
		if !ok {
			c.respondError(message.ID, -32000, "turn is not active")
			return
		}
		c.respond(message.ID, map[string]any{})
		c.server.broadcast(params.ThreadID, "turn/completed", map[string]any{
			"threadId": params.ThreadID, "turn": turn,
		})
	default:
		if len(message.ID) > 0 {
			c.respondError(message.ID, -32601, "unsupported mock method")
		}
	}
}

func threadResponse(thread Thread) map[string]any {
	return map[string]any{"thread": thread, "model": "mock-model", "modelProvider": "mock",
		"cwd": thread.CWD, "approvalPolicy": "never", "sandbox": map[string]any{"type": "dangerFullAccess"}}
}

func (c *connection) respond(id json.RawMessage, result any) {
	c.write(map[string]any{"id": json.RawMessage(id), "result": result})
}

func (c *connection) respondError(id json.RawMessage, code int, message string) {
	c.write(map[string]any{"id": json.RawMessage(id), "error": RPCError{Code: code, Message: message}})
}

func (c *connection) write(value any) {
	payload, _ := json.Marshal(value)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.ws.WriteMessage(websocket.TextMessage, payload)
}

func (s *Server) createThread(cwd string) Thread {
	id := "thread-" + jsonNumber(s.nextID.Add(1))
	thread := Thread{ID: id, CWD: cwd, Status: map[string]string{"type": "idle"}}
	s.mu.Lock()
	s.threads[id] = thread
	s.mu.Unlock()
	return thread
}

func (s *Server) listThreads() []Thread {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Thread, 0, len(s.threads))
	for _, thread := range s.threads {
		result = append(result, thread)
	}
	return result
}

func (s *Server) startTurn(threadID, clientID string) (Turn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	thread, ok := s.threads[threadID]
	if !ok {
		return Turn{}, fmt.Errorf("unknown thread")
	}
	for _, turn := range thread.Turns {
		if turn.Status == "inProgress" {
			return Turn{}, fmt.Errorf("turn already active")
		}
	}
	turn := Turn{ID: "turn-" + jsonNumber(s.nextID.Add(1)), ThreadID: threadID,
		Status: "inProgress", ClientUserMessageID: clientID}
	thread.Turns = append(thread.Turns, turn)
	thread.Status = map[string]string{"type": "active"}
	s.threads[threadID] = thread
	return turn, nil
}

func (s *Server) activeTurn(threadID, turnID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	thread, ok := s.threads[threadID]
	if !ok {
		return false
	}
	for _, turn := range thread.Turns {
		if turn.ID == turnID && turn.Status == "inProgress" {
			return true
		}
	}
	return false
}

func (s *Server) finishTurn(threadID, turnID, status string) (Turn, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	thread, ok := s.threads[threadID]
	if !ok {
		return Turn{}, false
	}
	for index := range thread.Turns {
		if thread.Turns[index].ID == turnID && thread.Turns[index].Status == "inProgress" {
			thread.Turns[index].Status = status
			thread.Status = map[string]string{"type": "idle"}
			s.threads[threadID] = thread
			return thread.Turns[index], true
		}
	}
	return Turn{}, false
}

func (s *Server) CompleteTurn(threadID, turnID, finalText string) bool {
	turn, ok := s.finishTurn(threadID, turnID, "completed")
	if !ok {
		return false
	}
	s.broadcast(threadID, "item/completed", map[string]any{
		"threadId": threadID, "turnId": turnID,
		"item": map[string]any{"id": "final-" + turnID, "type": "agentMessage",
			"phase": "final_answer", "text": finalText},
	})
	s.broadcast(threadID, "turn/completed", map[string]any{"threadId": threadID, "turn": turn})
	return true
}

func (s *Server) thread(id string) (Thread, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	thread, ok := s.threads[id]
	return thread, ok
}

func jsonNumber(value int64) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func (s *Server) broadcast(threadID, method string, params any) {
	s.broadcastMessage(threadID, map[string]any{"method": method, "params": params})
}

func (s *Server) broadcastMessage(threadID string, message any) {
	s.mu.Lock()
	connections := make([]*connection, 0, len(s.connections))
	for _, item := range s.connections {
		if item.subscriptions[threadID] {
			connections = append(connections, item)
		}
	}
	s.mu.Unlock()
	for _, item := range connections {
		item.write(message)
	}
}

func (s *Server) Emit(threadID, method string, params any) {
	s.broadcast(threadID, method, params)
}

func (s *Server) RequestUserInput(threadID, turnID, itemID string, questions any, autoResolutionMS int64) string {
	id := s.nextID.Add(1)
	key := strconv.FormatInt(id, 10)
	s.mu.Lock()
	s.pending[key] = pendingRequest{threadID: threadID}
	s.mu.Unlock()
	s.broadcastMessage(threadID, map[string]any{"id": id, "method": "item/tool/requestUserInput",
		"params": map[string]any{"threadId": threadID, "turnId": turnID, "itemId": itemID,
			"questions": questions, "autoResolutionMs": autoResolutionMS}})
	return key
}

func (s *Server) RequestDynamicTool(threadID, turnID, callID, namespace, tool string,
	arguments any,
) string {
	id := s.nextID.Add(1)
	key := strconv.FormatInt(id, 10)
	s.mu.Lock()
	s.pending[key] = pendingRequest{threadID: threadID}
	s.mu.Unlock()
	s.broadcastMessage(threadID, map[string]any{"id": id, "method": "item/tool/call",
		"params": map[string]any{"threadId": threadID, "turnId": turnID, "callId": callID,
			"namespace": namespace, "tool": tool, "arguments": arguments}})
	return key
}

func (s *Server) RequestServer(threadID, method string, params map[string]any) string {
	id := s.nextID.Add(1)
	key := strconv.FormatInt(id, 10)
	params["threadId"] = threadID
	s.mu.Lock()
	s.pending[key] = pendingRequest{threadID: threadID}
	s.mu.Unlock()
	s.broadcastMessage(threadID, map[string]any{"id": id, "method": method, "params": params})
	return key
}

func (s *Server) resolve(message Message) {
	key := string(message.ID)
	s.mu.Lock()
	pending, ok := s.pending[key]
	if ok {
		pending.responses++
	}
	winner := ok && !pending.resolved
	if winner {
		pending.resolved = true
		pending.result = append(json.RawMessage(nil), message.Result...)
	}
	if ok {
		s.pending[key] = pending
	}
	s.mu.Unlock()
	if winner {
		s.broadcast(pending.threadID, "serverRequest/resolved", map[string]any{
			"threadId": pending.threadID, "requestId": message.ID,
		})
	}
}

func (s *Server) ResolvedRequest(requestID string) (json.RawMessage, int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending, ok := s.pending[requestID]
	return append(json.RawMessage(nil), pending.result...), pending.responses, ok && pending.resolved
}

func DialContext(socketPath string) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", socketPath)
	}
}
