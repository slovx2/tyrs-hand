package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

const (
	fixtureCodexVersion  = "0.147.0"
	fixtureWorkspaceRoot = "/workspace"
)

type rpcMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type officialTurn struct {
	ID          string           `json:"id"`
	Items       []map[string]any `json:"items"`
	ItemsView   string           `json:"itemsView"`
	Status      string           `json:"status"`
	Error       any              `json:"error"`
	StartedAt   *int64           `json:"startedAt"`
	CompletedAt *int64           `json:"completedAt"`
	DurationMS  *int64           `json:"durationMs"`
	Prompt      string           `json:"-"`
}

type officialThread struct {
	ID                   string          `json:"id"`
	Extra                any             `json:"extra"`
	SessionID            string          `json:"sessionId"`
	ForkedFromID         *string         `json:"forkedFromId"`
	ParentThreadID       *string         `json:"parentThreadId"`
	Preview              string          `json:"preview"`
	Ephemeral            bool            `json:"ephemeral"`
	Section              any             `json:"section"`
	SectionEnteredAt     *int64          `json:"sectionEnteredAt"`
	HistoryMode          string          `json:"historyMode"`
	ModelProvider        string          `json:"modelProvider"`
	CreatedAt            int64           `json:"createdAt"`
	UpdatedAt            int64           `json:"updatedAt"`
	RecencyAt            *int64          `json:"recencyAt"`
	Status               map[string]any  `json:"status"`
	Path                 *string         `json:"path"`
	CWD                  string          `json:"cwd"`
	CLIVersion           string          `json:"cliVersion"`
	Source               string          `json:"source"`
	CanAcceptDirectInput bool            `json:"canAcceptDirectInput"`
	ThreadSource         *string         `json:"threadSource"`
	AgentNickname        *string         `json:"agentNickname"`
	AgentRole            *string         `json:"agentRole"`
	GitInfo              any             `json:"gitInfo"`
	Name                 *string         `json:"name"`
	Turns                []*officialTurn `json:"turns"`
	Archived             bool            `json:"-"`
}

type socketClient struct {
	connection  *websocket.Conn
	writeMu     sync.Mutex
	initialized bool
}

func (c *socketClient) write(value any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.connection.WriteJSON(value)
}

type pendingRequest struct {
	ID       string
	ThreadID string
	TurnID   string
	Payload  map[string]any
}

type appServerFixture struct {
	mu         sync.Mutex
	clients    map[*socketClient]struct{}
	threads    []*officialThread
	pending    map[string]*pendingRequest
	requestSeq int64
}

func newAppServerFixture() *appServerFixture {
	return &appServerFixture{clients: make(map[*socketClient]struct{}),
		pending: make(map[string]*pendingRequest)}
}

func (s *appServerFixture) serve(ctx context.Context, connection *websocket.Conn) error {
	client := &socketClient{connection: connection}
	s.mu.Lock()
	s.clients[client] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.clients, client)
		s.mu.Unlock()
		_ = connection.Close()
	}()
	for ctx.Err() == nil {
		_, payload, err := connection.ReadMessage()
		if err != nil {
			return err
		}
		var message rpcMessage
		if err = json.Unmarshal(payload, &message); err != nil {
			return fmt.Errorf("解析官方 JSON-RPC: %w", err)
		}
		if message.Method != "" {
			if len(message.ID) == 0 {
				if message.Method == "initialized" {
					s.mu.Lock()
					client.initialized = true
					s.mu.Unlock()
				}
				continue
			}
			s.handleRequest(client, message)
			continue
		}
		if len(message.ID) > 0 {
			s.handleServerResponse(message)
		}
	}
	return ctx.Err()
}

func (s *appServerFixture) handleRequest(client *socketClient, message rpcMessage) {
	switch message.Method {
	case "initialize":
		s.respond(client, message.ID, map[string]any{"userAgent": "mobile-e2e/" + fixtureCodexVersion,
			"codexHome": "/workspace/.codex", "platformFamily": "unix", "platformOs": "linux"})
	case "model/list":
		s.respond(client, message.ID, map[string]any{"data": []any{fixtureModel()}, "nextCursor": nil})
	case "thread/list":
		s.listThreads(client, message)
	case "thread/read":
		s.readThread(client, message)
	case "thread/resume":
		s.resumeThread(client, message)
	case "thread/turns/list":
		s.listThreadTurns(client, message)
	case "thread/start":
		s.startThread(client, message)
	case "thread/name/set":
		s.setThreadName(client, message)
	case "thread/archive", "thread/unarchive":
		s.setThreadArchived(client, message)
	case "turn/start":
		s.startTurn(client, message)
	case "turn/steer":
		s.steerTurn(client, message)
	case "turn/interrupt":
		s.interruptTurn(client, message)
	default:
		s.reject(client, message.ID, -32601, "fixture 未实现官方方法 "+message.Method)
	}
}

func (s *appServerFixture) respond(client *socketClient, id json.RawMessage, result any) {
	_ = client.write(map[string]any{"id": json.RawMessage(id), "result": result})
}

func (s *appServerFixture) reject(client *socketClient, id json.RawMessage, code int,
	message string,
) {
	_ = client.write(map[string]any{"id": json.RawMessage(id),
		"error": rpcError{Code: code, Message: message}})
}

func (s *appServerFixture) broadcast(value any) {
	s.mu.Lock()
	clients := make([]*socketClient, 0, len(s.clients))
	for client := range s.clients {
		if client.initialized {
			clients = append(clients, client)
		}
	}
	s.mu.Unlock()
	for _, client := range clients {
		_ = client.write(value)
	}
}

func (s *appServerFixture) findThreadLocked(id string) *officialThread {
	for _, thread := range s.threads {
		if thread.ID == id {
			return thread
		}
	}
	return nil
}

func cloneJSON(value any) map[string]any {
	encoded, _ := json.Marshal(value)
	var result map[string]any
	_ = json.Unmarshal(encoded, &result)
	return result
}

func threadSummary(thread *officialThread) map[string]any {
	result := cloneJSON(thread)
	result["turns"] = []any{}
	return result
}

func fixtureModel() map[string]any {
	efforts := []any{}
	for _, effort := range []string{"low", "medium", "high", "xhigh", "max", "ultra"} {
		efforts = append(efforts, map[string]any{"reasoningEffort": effort, "description": effort})
	}
	return map[string]any{"id": "gpt-5.6-sol", "model": "gpt-5.6-sol",
		"upgrade": nil, "upgradeInfo": nil, "availabilityNux": nil,
		"displayName": "GPT-5.6 Sol", "description": "移动端官方协议 E2E 模型",
		"modelSpecialty": nil, "hidden": false, "supportedReasoningEfforts": efforts,
		"defaultReasoningEffort": "high", "inputModalities": []string{"text", "image"},
		"supportsPersonality": true, "additionalSpeedTiers": []string{"fast"},
		"serviceTiers": []any{
			map[string]any{"id": "standard", "name": "标准", "description": "标准速度"},
			map[string]any{"id": "fast", "name": "快速", "description": "快速处理"},
		}, "defaultServiceTier": "standard", "isDefault": true}
}

func (s *appServerFixture) listThreads(client *socketClient, message rpcMessage) {
	var params struct {
		CWD      string  `json:"cwd"`
		Archived bool    `json:"archived"`
		Cursor   *string `json:"cursor"`
		Limit    int     `json:"limit"`
	}
	if json.Unmarshal(message.Params, &params) != nil {
		s.reject(client, message.ID, -32602, "thread/list 参数无效")
		return
	}
	s.mu.Lock()
	threads := make([]*officialThread, 0, len(s.threads))
	for _, thread := range s.threads {
		if thread.Archived != params.Archived || (params.CWD != "" && thread.CWD != params.CWD) {
			continue
		}
		threads = append(threads, thread)
	}
	sort.SliceStable(threads, func(left, right int) bool {
		if threads[left].UpdatedAt == threads[right].UpdatedAt {
			return threads[left].ID > threads[right].ID
		}
		return threads[left].UpdatedAt > threads[right].UpdatedAt
	})
	offset := 0
	if params.Cursor != nil {
		offset, _ = strconv.Atoi(*params.Cursor)
	}
	limit := params.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	end := min(offset+limit, len(threads))
	data := make([]any, 0, max(0, end-offset))
	if offset >= 0 && offset < len(threads) {
		for _, thread := range threads[offset:end] {
			data = append(data, threadSummary(thread))
		}
	}
	var nextCursor any
	if end < len(threads) {
		nextCursor = strconv.Itoa(end)
	}
	s.mu.Unlock()
	s.respond(client, message.ID, map[string]any{"data": data, "nextCursor": nextCursor})
}

func (s *appServerFixture) readThread(client *socketClient, message rpcMessage) {
	var params struct {
		ThreadID     string `json:"threadId"`
		IncludeTurns *bool  `json:"includeTurns"`
	}
	if json.Unmarshal(message.Params, &params) != nil || params.ThreadID == "" {
		s.reject(client, message.ID, -32602, "thread/read 参数无效")
		return
	}
	s.mu.Lock()
	thread := s.findThreadLocked(params.ThreadID)
	if thread == nil {
		s.mu.Unlock()
		s.reject(client, message.ID, -32602, "thread 不存在")
		return
	}
	result := cloneJSON(thread)
	if params.IncludeTurns != nil && !*params.IncludeTurns {
		result["turns"] = []any{}
	}
	s.mu.Unlock()
	s.respond(client, message.ID, map[string]any{"thread": result})
}

func (s *appServerFixture) resumeThread(client *socketClient, message rpcMessage) {
	var params struct {
		ThreadID         string `json:"threadId"`
		ExcludeTurns     bool   `json:"excludeTurns"`
		InitialTurnsPage *struct {
			Limit         int    `json:"limit"`
			SortDirection string `json:"sortDirection"`
			ItemsView     string `json:"itemsView"`
		} `json:"initialTurnsPage"`
	}
	if json.Unmarshal(message.Params, &params) != nil || params.ThreadID == "" {
		s.reject(client, message.ID, -32602, "thread/resume 参数无效")
		return
	}
	s.mu.Lock()
	thread := s.findThreadLocked(params.ThreadID)
	if thread == nil {
		s.mu.Unlock()
		s.reject(client, message.ID, -32602, "thread 不存在")
		return
	}
	result := cloneJSON(thread)
	if params.ExcludeTurns {
		result["turns"] = []any{}
	}
	var initialPage any
	if params.InitialTurnsPage != nil {
		page, err := turnsPage(thread, nil, params.InitialTurnsPage.Limit,
			params.InitialTurnsPage.SortDirection, params.InitialTurnsPage.ItemsView)
		if err != nil {
			s.mu.Unlock()
			s.reject(client, message.ID, -32602, err.Error())
			return
		}
		initialPage = page
	}
	pending := make([]map[string]any, 0)
	for _, request := range s.pending {
		if request.ThreadID == params.ThreadID {
			pending = append(pending, request.Payload)
		}
	}
	var turnsBackwardsCursor any
	if len(thread.Turns) > 0 {
		turnsBackwardsCursor = thread.Turns[len(thread.Turns)-1].ID
	}
	s.mu.Unlock()
	response := resumeResult(result, params.ThreadID)
	response["initialTurnsPage"] = initialPage
	response["turnsBackwardsCursor"] = turnsBackwardsCursor
	s.respond(client, message.ID, response)
	for _, request := range pending {
		_ = client.write(request)
	}
}

func (s *appServerFixture) listThreadTurns(client *socketClient, message rpcMessage) {
	var params struct {
		ThreadID      string  `json:"threadId"`
		Cursor        *string `json:"cursor"`
		Limit         int     `json:"limit"`
		SortDirection string  `json:"sortDirection"`
		ItemsView     string  `json:"itemsView"`
	}
	if json.Unmarshal(message.Params, &params) != nil || params.ThreadID == "" {
		s.reject(client, message.ID, -32602, "thread/turns/list 参数无效")
		return
	}
	s.mu.Lock()
	thread := s.findThreadLocked(params.ThreadID)
	if thread == nil {
		s.mu.Unlock()
		s.reject(client, message.ID, -32602, "thread 不存在")
		return
	}
	page, err := turnsPage(thread, params.Cursor, params.Limit, params.SortDirection,
		params.ItemsView)
	s.mu.Unlock()
	if err != nil {
		s.reject(client, message.ID, -32602, err.Error())
		return
	}
	s.respond(client, message.ID, page)
}

func turnsPage(thread *officialThread, cursor *string, limit int,
	sortDirection, itemsView string,
) (map[string]any, error) {
	if sortDirection == "" {
		sortDirection = "desc"
	}
	if sortDirection != "desc" && sortDirection != "asc" {
		return nil, errors.New("thread/turns/list sortDirection 无效")
	}
	if itemsView == "" {
		itemsView = "summary"
	}
	if itemsView != "summary" && itemsView != "full" {
		return nil, errors.New("thread/turns/list itemsView 无效")
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	start := 0
	step := 1
	if sortDirection == "desc" {
		start = len(thread.Turns) - 1
		step = -1
	}
	if cursor != nil {
		start = -1
		for index, turn := range thread.Turns {
			if turn.ID == *cursor {
				start = index
				break
			}
		}
		if start < 0 {
			return nil, errors.New("thread/turns/list cursor 无效")
		}
	}
	data := make([]any, 0, limit)
	index := start
	for index >= 0 && index < len(thread.Turns) && len(data) < limit {
		turn := cloneJSON(thread.Turns[index])
		turn["itemsView"] = itemsView
		data = append(data, turn)
		index += step
	}
	var nextCursor any
	if index >= 0 && index < len(thread.Turns) {
		nextCursor = thread.Turns[index].ID
	}
	var backwardsCursor any
	if len(data) > 0 {
		backwardsCursor = thread.Turns[start].ID
	}
	return map[string]any{"data": data, "nextCursor": nextCursor,
		"backwardsCursor": backwardsCursor}, nil
}

func resumeResult(thread map[string]any, threadID string) map[string]any {
	cwd, _ := thread["cwd"].(string)
	return map[string]any{"thread": thread, "model": "gpt-5.6-sol", "modelProvider": "openai",
		"serviceTier": "standard", "cwd": cwd, "runtimeWorkspaceRoots": []string{cwd},
		"instructionSources": []string{}, "approvalPolicy": "never", "approvalsReviewer": "user",
		"sandbox": map[string]any{"type": "dangerFullAccess"}, "activePermissionProfile": nil,
		"reasoningEffort": "high", "multiAgentMode": "explicitRequestOnly",
		"initialTurnsPage": nil, "turnsBackwardsCursor": nil, "itemsBackwardsCursor": nil,
		"threadId": threadID}
}

func (s *appServerFixture) startThread(client *socketClient, message rpcMessage) {
	var params struct {
		CWD          string  `json:"cwd"`
		ThreadSource *string `json:"threadSource"`
	}
	if json.Unmarshal(message.Params, &params) != nil || !strings.HasPrefix(params.CWD, "/") {
		s.reject(client, message.ID, -32602, "thread/start cwd 无效")
		return
	}
	now := time.Now().Unix()
	id := uuid.NewString()
	thread := &officialThread{ID: id, Extra: nil, SessionID: id, Preview: "新的开发任务",
		Ephemeral: false, Section: nil, HistoryMode: "legacy", ModelProvider: "openai",
		CreatedAt: now, UpdatedAt: now, RecencyAt: &now, Status: map[string]any{"type": "idle"},
		CWD: params.CWD, CLIVersion: fixtureCodexVersion, Source: "appServer",
		CanAcceptDirectInput: true, ThreadSource: params.ThreadSource, GitInfo: nil,
		Turns: make([]*officialTurn, 0)}
	s.mu.Lock()
	s.threads = append(s.threads, thread)
	responseThread := threadSummary(thread)
	s.mu.Unlock()
	s.respond(client, message.ID, resumeResult(responseThread, id))
	s.broadcast(map[string]any{"method": "thread/started",
		"params": map[string]any{"thread": responseThread}})
}

func (s *appServerFixture) setThreadName(client *socketClient, message rpcMessage) {
	var params struct {
		ThreadID string `json:"threadId"`
		Name     string `json:"name"`
	}
	if json.Unmarshal(message.Params, &params) != nil || strings.TrimSpace(params.Name) == "" {
		s.reject(client, message.ID, -32602, "thread/name/set 参数无效")
		return
	}
	name := strings.TrimSpace(params.Name)
	s.mu.Lock()
	thread := s.findThreadLocked(params.ThreadID)
	if thread != nil {
		thread.Name = &name
		thread.UpdatedAt = time.Now().Unix()
	}
	s.mu.Unlock()
	if thread == nil {
		s.reject(client, message.ID, -32602, "thread 不存在")
		return
	}
	s.respond(client, message.ID, map[string]any{})
	s.broadcast(map[string]any{"method": "thread/name/updated",
		"params": map[string]any{"threadId": params.ThreadID, "threadName": name}})
}

func (s *appServerFixture) setThreadArchived(client *socketClient, message rpcMessage) {
	var params struct {
		ThreadID string `json:"threadId"`
	}
	if json.Unmarshal(message.Params, &params) != nil || params.ThreadID == "" {
		s.reject(client, message.ID, -32602, "thread lifecycle 参数无效")
		return
	}
	archived := message.Method == "thread/archive"
	s.mu.Lock()
	thread := s.findThreadLocked(params.ThreadID)
	if thread != nil {
		thread.Archived = archived
		thread.UpdatedAt = time.Now().Unix()
	}
	var responseThread map[string]any
	if thread != nil {
		responseThread = threadSummary(thread)
	}
	s.mu.Unlock()
	if thread == nil {
		s.reject(client, message.ID, -32602, "thread 不存在")
		return
	}
	if archived {
		s.respond(client, message.ID, map[string]any{})
		s.broadcast(map[string]any{"method": "thread/archived",
			"params": map[string]any{"threadId": params.ThreadID}})
		return
	}
	s.respond(client, message.ID, map[string]any{"thread": responseThread})
	s.broadcast(map[string]any{"method": "thread/unarchived",
		"params": map[string]any{"threadId": params.ThreadID}})
}

func (s *appServerFixture) startTurn(client *socketClient, message rpcMessage) {
	var params struct {
		ThreadID          string           `json:"threadId"`
		ClientMessageID   *string          `json:"clientUserMessageId"`
		Input             []map[string]any `json:"input"`
		CollaborationMode struct {
			Mode string `json:"mode"`
		} `json:"collaborationMode"`
	}
	if json.Unmarshal(message.Params, &params) != nil || params.ThreadID == "" {
		s.reject(client, message.ID, -32602, "turn/start 参数无效")
		return
	}
	now := time.Now().Unix()
	prompt := inputText(params.Input)
	s.mu.Lock()
	thread := s.findThreadLocked(params.ThreadID)
	if thread == nil {
		s.mu.Unlock()
		s.reject(client, message.ID, -32602, "thread 不存在")
		return
	}
	if activeTurn(thread) != nil {
		s.mu.Unlock()
		s.reject(client, message.ID, -32600, "thread already has an active turn")
		return
	}
	turn := &officialTurn{ID: uuid.NewString(), ItemsView: "full", Status: "inProgress",
		StartedAt: &now, Prompt: prompt, Items: []map[string]any{{"type": "userMessage",
			"id": uuid.NewString(), "clientId": params.ClientMessageID, "content": params.Input}}}
	thread.Turns = append(thread.Turns, turn)
	thread.Status = map[string]any{"type": "active", "activeFlags": []string{}}
	thread.UpdatedAt, thread.RecencyAt = now, &now
	if prompt != "" {
		thread.Preview = prompt
	}
	turnResult := cloneJSON(turn)
	s.mu.Unlock()
	s.respond(client, message.ID, map[string]any{"turn": turnResult})
	s.broadcast(map[string]any{"method": "turn/started",
		"params": map[string]any{"threadId": params.ThreadID, "turn": turnResult}})
	s.dispatchTurn(params.ThreadID, turn.ID, prompt, params.CollaborationMode.Mode)
}

func (s *appServerFixture) steerTurn(client *socketClient, message rpcMessage) {
	var params struct {
		ThreadID        string           `json:"threadId"`
		ExpectedTurnID  string           `json:"expectedTurnId"`
		ClientMessageID *string          `json:"clientUserMessageId"`
		Input           []map[string]any `json:"input"`
	}
	if json.Unmarshal(message.Params, &params) != nil || params.ThreadID == "" {
		s.reject(client, message.ID, -32602, "turn/steer 参数无效")
		return
	}
	s.mu.Lock()
	thread := s.findThreadLocked(params.ThreadID)
	turn := activeTurn(thread)
	if turn == nil {
		s.mu.Unlock()
		s.reject(client, message.ID, -32600, "no active turn to steer")
		return
	}
	if turn.ID != params.ExpectedTurnID {
		actual := turn.ID
		s.mu.Unlock()
		s.reject(client, message.ID, -32600,
			"expected active turn id "+params.ExpectedTurnID+" but found "+actual)
		return
	}
	prompt := inputText(params.Input)
	turn.Items = append(turn.Items, map[string]any{"type": "userMessage", "id": uuid.NewString(),
		"clientId": params.ClientMessageID, "content": params.Input})
	thread.UpdatedAt = time.Now().Unix()
	updated := cloneJSON(turn)
	s.mu.Unlock()
	s.respond(client, message.ID, map[string]any{"turnId": turn.ID})
	s.broadcast(map[string]any{"method": "turn/started",
		"params": map[string]any{"threadId": params.ThreadID, "turn": updated}})
	if !strings.Contains(turn.Prompt, "E2E_BLOCK") {
		go func() {
			time.Sleep(180 * time.Millisecond)
			s.completeTurn(params.ThreadID, turn.ID, prompt, false)
		}()
	}
}

func (s *appServerFixture) interruptTurn(client *socketClient, message rpcMessage) {
	var params struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
	}
	if json.Unmarshal(message.Params, &params) != nil || params.TurnID == "" {
		s.reject(client, message.ID, -32602, "turn/interrupt 参数无效")
		return
	}
	s.mu.Lock()
	thread := s.findThreadLocked(params.ThreadID)
	turn := findTurn(thread, params.TurnID)
	if turn != nil && turn.Status == "inProgress" {
		now := time.Now().Unix()
		duration := (now - *turn.StartedAt) * 1000
		turn.Status, turn.CompletedAt, turn.DurationMS = "interrupted", &now, &duration
		thread.Status = map[string]any{"type": "idle"}
		thread.UpdatedAt, thread.RecencyAt = now, &now
	}
	var completed map[string]any
	if turn != nil {
		completed = cloneJSON(turn)
	}
	s.mu.Unlock()
	if turn == nil {
		s.reject(client, message.ID, -32602, "turn 不存在")
		return
	}
	s.respond(client, message.ID, map[string]any{})
	s.broadcast(map[string]any{"method": "turn/completed",
		"params": map[string]any{"threadId": params.ThreadID, "turn": completed}})
}

func inputText(inputs []map[string]any) string {
	parts := make([]string, 0, len(inputs))
	for _, input := range inputs {
		if input["type"] == "text" {
			if text, ok := input["text"].(string); ok {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func activeTurn(thread *officialThread) *officialTurn {
	if thread == nil {
		return nil
	}
	for index := len(thread.Turns) - 1; index >= 0; index-- {
		if thread.Turns[index].Status == "inProgress" {
			return thread.Turns[index]
		}
	}
	return nil
}

func findTurn(thread *officialThread, id string) *officialTurn {
	if thread == nil {
		return nil
	}
	for _, turn := range thread.Turns {
		if turn.ID == id {
			return turn
		}
	}
	return nil
}

func (s *appServerFixture) dispatchTurn(threadID, turnID, prompt, mode string) {
	if strings.Contains(prompt, "E2E_BLOCK") {
		return
	}
	if strings.Contains(prompt, "E2E_ASK") || strings.Contains(prompt, "E2E_SECRET") {
		go func() {
			time.Sleep(120 * time.Millisecond)
			s.requestUserInput(threadID, turnID, prompt)
		}()
		return
	}
	delay := 180 * time.Millisecond
	if strings.Contains(prompt, "E2E_RESTART") {
		delay = 10 * time.Second
	}
	go func() {
		time.Sleep(delay)
		s.completeTurn(threadID, turnID, prompt, strings.Contains(prompt, "E2E_FAIL_429"))
	}()
	_ = mode
}

func (s *appServerFixture) requestUserInput(threadID, turnID, prompt string) {
	s.mu.Lock()
	thread := s.findThreadLocked(threadID)
	turn := findTurn(thread, turnID)
	if turn == nil || turn.Status != "inProgress" {
		s.mu.Unlock()
		return
	}
	s.requestSeq++
	id := fmt.Sprintf("e2e-request-%d", s.requestSeq)
	itemID := uuid.NewString()
	questions := []any{}
	if strings.Contains(prompt, "E2E_SECRET") {
		questions = append(questions, map[string]any{"id": "secret", "header": "Secret",
			"question": "请输入测试 Secret", "isOther": true, "isSecret": true})
	} else {
		questions = append(questions, map[string]any{"id": "scope", "header": "确认",
			"question": "继续执行 E2E 吗？", "isOther": true, "isSecret": false,
			"options": []any{
				map[string]any{"label": "继续", "description": "完成官方协议闭环。"},
				map[string]any{"label": "停止", "description": "结束当前任务。"},
			}})
		if strings.Contains(prompt, "E2E_ASK_MATRIX") {
			questions = append(questions, map[string]any{"id": "note", "header": "补充说明",
				"question": "请输入移动端补充说明", "isOther": true, "isSecret": false})
		}
	}
	payload := map[string]any{"id": id, "method": "item/tool/requestUserInput",
		"params": map[string]any{"threadId": threadID, "turnId": turnID, "itemId": itemID,
			"isBlocking": true, "autoResolutionMs": nil, "questions": questions}}
	s.pending[`"`+id+`"`] = &pendingRequest{ID: id, ThreadID: threadID, TurnID: turnID,
		Payload: payload}
	thread.Status = map[string]any{"type": "active", "activeFlags": []string{"waitingOnUserInput"}}
	s.mu.Unlock()
	s.broadcast(payload)
}

func (s *appServerFixture) handleServerResponse(message rpcMessage) {
	key := string(message.ID)
	s.mu.Lock()
	request := s.pending[key]
	if request != nil {
		delete(s.pending, key)
	}
	s.mu.Unlock()
	if request == nil {
		return
	}
	s.broadcast(map[string]any{"method": "serverRequest/resolved", "params": map[string]any{
		"threadId": request.ThreadID, "requestId": request.ID,
	}})
	s.completeTurn(request.ThreadID, request.TurnID,
		"已收到移动端回答，交互闭环完成。", false)
}

func (s *appServerFixture) completeTurn(threadID, turnID, prompt string, failed bool) {
	s.mu.Lock()
	thread := s.findThreadLocked(threadID)
	turn := findTurn(thread, turnID)
	if turn == nil || turn.Status != "inProgress" {
		s.mu.Unlock()
		return
	}
	now := time.Now().Unix()
	duration := (now - *turn.StartedAt) * 1000
	if duration <= 0 {
		duration = 50
	}
	if failed {
		turn.Status = "failed"
		turn.Error = map[string]any{"message": "deterministic rate limit",
			"codexErrorInfo": nil, "additionalDetails": nil}
	} else {
		turn.Items = append(turn.Items, completionItems(prompt, turn.ID)...)
		turn.Status = "completed"
	}
	turn.CompletedAt, turn.DurationMS = &now, &duration
	thread.Status = map[string]any{"type": "idle"}
	thread.UpdatedAt, thread.RecencyAt = now, &now
	completed := cloneJSON(turn)
	s.mu.Unlock()
	s.broadcast(map[string]any{"method": "turn/completed",
		"params": map[string]any{"threadId": threadID, "turn": completed}})
}

func completionItems(prompt, turnID string) []map[string]any {
	if strings.Contains(prompt, "E2E_PLAN") &&
		!strings.HasPrefix(prompt, "PLEASE IMPLEMENT THIS PLAN:") {
		return []map[string]any{{"type": "plan", "id": uuid.NewString(),
			"text": "1. 验证参数\n2. 执行任务\n3. 返回结果"}}
	}
	items := make([]map[string]any, 0, 8)
	if strings.Contains(prompt, "E2E_EVENTS") {
		items = append(items,
			map[string]any{"type": "reasoning", "id": uuid.NewString(),
				"summary": []string{"已核对官方 Thread 顺序。"}, "content": []string{}},
			map[string]any{"type": "commandExecution", "id": uuid.NewString(),
				"pluginId": nil, "scriptPath": nil, "command": "pnpm typecheck",
				"cwd": "/workspace/e2e-project", "processId": nil, "source": "agent",
				"status": "completed", "commandActions": []any{},
				"aggregatedOutput": "类型检查通过", "exitCode": 0, "durationMs": 25},
			map[string]any{"type": "fileChange", "id": uuid.NewString(), "status": "completed",
				"changes": []any{map[string]any{"path": "client/src/features/chat/OfficialTurn.tsx",
					"kind": "update", "diff": "@@ official item @@"}}},
			map[string]any{"type": "mcpToolCall", "id": uuid.NewString(), "server": "filesystem",
				"tool": "read_file", "status": "completed", "arguments": map[string]any{},
				"appContext": nil, "pluginId": nil, "readOnlyHint": true,
				"result": map[string]any{"content": []any{}, "structuredContent": nil, "_meta": nil},
				"error":  nil, "durationMs": 10},
			map[string]any{"type": "webSearch", "id": uuid.NewString(),
				"query": "Tyrs Hand mobile E2E", "action": nil},
			map[string]any{"type": "collabAgentToolCall", "id": uuid.NewString(),
				"tool": "spawnAgent", "status": "completed", "senderThreadId": turnID,
				"receiverThreadIds": []string{"fixture-agent"}, "prompt": "验证官方协议",
				"model": nil, "reasoningEffort": nil, "agentsStates": map[string]any{}},
		)
	}
	answer := "官方 App Server fixture 已完成。"
	switch {
	case strings.Contains(prompt, "交互闭环完成"):
		answer = prompt
	case strings.Contains(prompt, "E2E_EVENTS"):
		answer = "## 事件矩阵完成\n\n官方 Item 已按 Turn 顺序展示。"
	case strings.HasPrefix(prompt, "PLEASE IMPLEMENT THIS PLAN:"):
		answer = "计划已按官方 Default Turn 执行。"
	}
	items = append(items, map[string]any{"type": "agentMessage", "id": uuid.NewString(),
		"text": answer, "phase": "final_answer", "memoryCitation": nil})
	return items
}

func heartbeatLoop(ctx context.Context, client *workerprotocol.Client,
	metadata json.RawMessage,
) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := client.Heartbeat(ctx, workerprotocol.HeartbeatRequest{
				WorkerVersion:   "mobile-e2e-official-app-server",
				ProtocolVersion: workerprotocol.Version, Metadata: metadata,
			}); err != nil && ctx.Err() == nil {
				log.Printf("协议 25 心跳失败：%v", err)
			}
		}
	}
}

func materializationLoop(ctx context.Context, client *workerprotocol.Client) {
	for ctx.Err() == nil {
		claimCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		claim, err := client.ClaimMaterialization(claimCtx)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("领取 E2E materialization 失败：%v", err)
				time.Sleep(time.Second)
			}
			continue
		}
		if claim.Materialization == nil {
			time.Sleep(250 * time.Millisecond)
			continue
		}
		processMaterialization(ctx, client, claim.Materialization)
	}
}

func processMaterialization(ctx context.Context, client *workerprotocol.Client,
	task *workerprotocol.MaterializationClaim,
) {
	deadline := task.ExpiresAt
	if deadline.IsZero() {
		deadline = time.Now().Add(2 * time.Minute)
	}
	materializeCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	hash := sha256.New()
	headerDigest, written, err := client.DownloadMaterialization(materializeCtx, task, hash)
	computed := hex.EncodeToString(hash.Sum(nil))
	if err == nil && (written != task.SizeBytes || computed != strings.ToLower(task.SHA256) ||
		!strings.EqualFold(strings.TrimSpace(headerDigest), task.SHA256)) {
		err = errors.New("materialization 内容长度或 SHA-256 不匹配")
	}
	if err == nil {
		err = client.CompleteMaterialization(materializeCtx, task.ID,
			workerprotocol.MaterializationCompleteRequest{LeaseToken: task.LeaseToken,
				RemotePath: fixtureWorkspaceRoot + "/.cache/tyrs-hand/" + computed})
	}
	if err == nil {
		return
	}
	failCtx, failCancel := context.WithTimeout(ctx, 10*time.Second)
	defer failCancel()
	_ = client.FailMaterialization(failCtx, task.ID, workerprotocol.MaterializationFailRequest{
		LeaseToken: task.LeaseToken, Error: err.Error()})
}

func tunnelLoop(ctx context.Context, client *workerprotocol.Client,
	fixture *appServerFixture,
) error {
	for ctx.Err() == nil {
		claimCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		claim, err := client.ClaimAppServerTunnel(claimCtx)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			log.Printf("领取官方 tunnel 失败：%v", err)
			time.Sleep(time.Second)
			continue
		}
		if claim.Tunnel == nil {
			continue
		}
		connectCtx, connectCancel := context.WithDeadline(ctx, claim.Tunnel.ExpiresAt)
		connection, connectErr := client.OpenAppServerTunnel(connectCtx, claim.Tunnel.ID)
		connectCancel()
		if connectErr != nil {
			log.Printf("连接官方 tunnel 失败：%v", connectErr)
			continue
		}
		go func() {
			if serveErr := fixture.serve(ctx, connection); serveErr != nil && ctx.Err() == nil &&
				!websocket.IsCloseError(serveErr, websocket.CloseNormalClosure) {
				log.Printf("官方 tunnel 已断开：%v", serveErr)
			}
		}()
	}
	return ctx.Err()
}

func main() {
	baseURL := strings.TrimRight(os.Getenv("TYRS_HAND_E2E_CONTROL_URL"), "/")
	enrollmentToken := os.Getenv("TYRS_HAND_E2E_ENROLLMENT_TOKEN")
	workerLabel := os.Getenv("TYRS_HAND_E2E_WORKER_ID")
	if baseURL == "" || enrollmentToken == "" {
		log.Fatal("协议 Worker 缺少 Control URL 或 Enrollment Token")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	client := workerprotocol.NewClient(baseURL, "", 40*time.Second)
	enrolled, err := client.Enroll(ctx, enrollmentToken)
	if err != nil {
		log.Fatalf("注册协议 25 Worker：%v", err)
	}
	if enrolled.ProtocolVersion != workerprotocol.Version {
		log.Fatalf("Control 返回 Worker 协议 %d，预期 %d", enrolled.ProtocolVersion,
			workerprotocol.Version)
	}
	client.SetCredential(enrolled.Credential)
	workspace, err := client.Workspace(ctx)
	if err != nil || workspace == nil {
		log.Fatalf("读取协议 Worker Workspace：%v", err)
	}
	metadata, err := json.Marshal(map[string]any{"lane": "mobile-official-protocol",
		"label": workerLabel, "host": map[string]any{"workspaceRoot": fixtureWorkspaceRoot,
			"appServer": "running", "codexVersion": fixtureCodexVersion},
		"modelCatalogs": map[string]any{workspace.WorkspaceID.String(): map[string]any{
			"data": []any{fixtureModel()}, "nextCursor": nil,
		}}})
	if err != nil {
		log.Fatal(err)
	}
	if err = client.Heartbeat(ctx, workerprotocol.HeartbeatRequest{
		WorkerVersion: "mobile-e2e-official-app-server", ProtocolVersion: workerprotocol.Version,
		Metadata: metadata,
	}); err != nil {
		log.Fatalf("发布协议 25 初始心跳：%v", err)
	}
	go heartbeatLoop(ctx, client, metadata)
	go materializationLoop(ctx, client)
	if err = tunnelLoop(ctx, client, newAppServerFixture()); err != nil &&
		!errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}
