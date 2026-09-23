package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"go.uber.org/zap"
)

type workerRPCConnection struct {
	workerID     uuid.UUID
	conn         *websocket.Conn
	writeMu      sync.Mutex
	mu           sync.Mutex
	pending      map[string]chan workerprotocol.WorkerRPCResponse
	capabilities map[string]bool
	closed       chan struct{}
}

const workerChannelPingInterval = 20 * time.Second

func (s *Server) workerRPCWS(c *gin.Context) {
	worker := currentWorker(c)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	state := &workerRPCConnection{workerID: worker.ID, conn: conn,
		pending:      make(map[string]chan workerprotocol.WorkerRPCResponse),
		capabilities: make(map[string]bool), closed: make(chan struct{})}
	s.workerRPCMu.Lock()
	if old := s.workerRPCConns[worker.ID]; old != nil {
		_ = old.conn.Close()
	}
	s.workerRPCConns[worker.ID] = state
	s.workerRPCMu.Unlock()
	defer func() {
		close(state.closed)
		_ = conn.Close()
		s.workerRPCMu.Lock()
		if s.workerRPCConns[worker.ID] == state {
			delete(s.workerRPCConns, worker.ID)
		}
		s.workerRPCMu.Unlock()
		state.mu.Lock()
		for id, wait := range state.pending {
			close(wait)
			delete(state.pending, id)
		}
		state.mu.Unlock()
	}()
	go func() {
		ticker := time.NewTicker(workerChannelPingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-state.closed:
				return
			case <-c.Request.Context().Done():
				return
			case <-ticker.C:
				if err := conn.WriteControl(websocket.PingMessage, nil,
					time.Now().Add(10*time.Second)); err != nil {
					_ = conn.Close()
					return
				}
			}
		}
	}()
	for {
		_, payload, readErr := conn.ReadMessage()
		if readErr != nil {
			return
		}
		var envelope struct {
			Type   string          `json:"type"`
			ID     string          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(payload, &envelope) != nil {
			continue
		}
		if envelope.Type == workerprotocol.MessageTypeRequest {
			s.handleWorkerChannelRequest(c.Request.Context(), state, envelope.ID,
				envelope.Method, envelope.Params)
			continue
		}
		if envelope.ID == "" {
			continue
		}
		var response workerprotocol.WorkerRPCResponse
		if json.Unmarshal(payload, &response) != nil {
			continue
		}
		state.mu.Lock()
		wait := state.pending[response.ID]
		if wait != nil {
			delete(state.pending, response.ID)
		}
		state.mu.Unlock()
		if wait != nil {
			wait <- response
			close(wait)
		}
	}
}

// handleWorkerChannelRequest 处理 Worker 主动发起的控制通道请求，目前只有 hello。
func (s *Server) handleWorkerChannelRequest(ctx context.Context,
	state *workerRPCConnection, id, method string, params json.RawMessage,
) {
	response := workerprotocol.WorkerRPCResponse{
		Type: workerprotocol.MessageTypeResponse, ID: id}
	if method != "hello" {
		response.Error = fmt.Sprintf("不支持的控制通道方法 %q", method)
	} else {
		var hello workerprotocol.WorkerHelloRequest
		if err := json.Unmarshal(params, &hello); err != nil {
			response.Error = err.Error()
		} else {
			state.mu.Lock()
			state.capabilities = make(map[string]bool, len(hello.Capabilities))
			for _, capability := range hello.Capabilities {
				state.capabilities[capability] = true
			}
			state.mu.Unlock()
			response.Result = workerprotocol.WorkerHelloResponse{
				Capabilities: []string{workerprotocol.WakeCapability}}
			if err := s.workers.Touch(ctx, state.workerID); err != nil && ctx.Err() == nil {
				if s.logger != nil {
					s.logger.Warn("刷新 Worker 在线时间失败", zap.Error(err))
				}
			}
		}
	}
	payload, err := json.Marshal(response)
	if err != nil {
		return
	}
	_ = state.write(payload)
}

func (c *workerRPCConnection) write(payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	if err := c.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		_ = c.conn.Close()
		return err
	}
	return nil
}

func (c *workerRPCConnection) supportsWake() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.capabilities[workerprotocol.WakeCapability]
}

// notify 向该 Worker 推送唤醒通知；未协商唤醒能力时静默忽略。
func (c *workerRPCConnection) notify(kinds []string) error {
	if len(kinds) == 0 || !c.supportsWake() {
		return nil
	}
	payload, err := json.Marshal(workerprotocol.WorkerNotification{
		Type: workerprotocol.MessageTypeNotify, Kinds: kinds})
	if err != nil {
		return err
	}
	return c.write(payload)
}

// notifyWorkerKinds 向指定 Worker 推送唤醒，未连接或未协商时返回 false。
func (s *Server) notifyWorkerKinds(workerID uuid.UUID, kinds []string) bool {
	s.workerRPCMu.RLock()
	state := s.workerRPCConns[workerID]
	s.workerRPCMu.RUnlock()
	if state == nil || !state.supportsWake() {
		return false
	}
	return state.notify(kinds) == nil
}

func (s *Server) callWorkerRPC(ctx context.Context, workerID uuid.UUID, method string,
	params any, timeout time.Duration,
) (any, error) {
	s.workerRPCMu.RLock()
	state := s.workerRPCConns[workerID]
	s.workerRPCMu.RUnlock()
	if state == nil {
		return nil, errors.New("Worker RPC 通道未连接")
	}
	id := uuid.NewString()
	wait := make(chan workerprotocol.WorkerRPCResponse, 1)
	state.mu.Lock()
	state.pending[id] = wait
	state.mu.Unlock()
	requestParams, _ := json.Marshal(params)
	request := workerprotocol.WorkerRPCRequest{ID: id, Method: method, Params: requestParams}
	payload, _ := json.Marshal(request)
	if err := state.write(payload); err != nil {
		state.mu.Lock()
		delete(state.pending, id)
		state.mu.Unlock()
		return nil, err
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case response, ok := <-wait:
		if !ok {
			return nil, errors.New("Worker RPC 通道已断开")
		}
		if response.Error != "" {
			return nil, errors.New(response.Error)
		}
		return response.Result, nil
	case <-ctx.Done():
		state.mu.Lock()
		delete(state.pending, id)
		state.mu.Unlock()
		return nil, ctx.Err()
	case <-timer.C:
		state.mu.Lock()
		delete(state.pending, id)
		state.mu.Unlock()
		return nil, errors.New("Worker RPC 请求超时")
	}
}

func (s *Server) callWorkerConfig(ctx context.Context, workerID uuid.UUID,
	method string, params any,
) (any, error) {
	return s.callWorkerRPC(ctx, workerID, method, params, 30*time.Second)
}

func (s *Server) workerConfig(c *gin.Context, method string, params any) {
	workerID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	if !s.requireWorkerAccess(c, workerID) {
		return
	}
	result, err := s.callWorkerConfig(c.Request.Context(), workerID, method, params)
	if err != nil {
		status := http.StatusBadGateway
		if err.Error() == "配置版本冲突" {
			status = http.StatusConflict
		} else if method == "config.provider.write" && isProviderValidationError(err) {
			status = http.StatusBadRequest
		}
		problem(c, status, fmt.Sprintf("Worker 配置操作失败: %s", method), err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func isProviderValidationError(err error) bool {
	message := err.Error()
	for _, marker := range []string{"Base URL", "API Key", "Model Provider", "配置长度"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func (s *Server) getWorkerConfig(c *gin.Context) { s.workerConfig(c, "config.read", nil) }

func (s *Server) updateWorkerAgents(c *gin.Context) {
	var request struct {
		Revision string `json:"revision"`
		Content  string `json:"content"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	s.workerConfig(c, "config.agents.write", request)
}

func (s *Server) updateWorkerProvider(c *gin.Context) {
	var request struct {
		Revision    string `json:"revision"`
		BaseURL     string `json:"baseUrl"`
		APIKey      string `json:"apiKey"`
		ClearAPIKey bool   `json:"clearApiKey"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	s.workerConfig(c, "config.provider.write", request)
}

func (s *Server) workerOAuthStart(c *gin.Context) { s.workerConfig(c, "oauth.devices.start", nil) }

func (s *Server) workerOAuthStatus(c *gin.Context) { s.workerConfig(c, "oauth.devices.status", nil) }

func (s *Server) workerOAuthLogout(c *gin.Context) { s.workerConfig(c, "oauth.logout", nil) }

func (s *Server) workerCodexRestart(c *gin.Context) { s.workerConfig(c, "codex.restart", nil) }
