package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

type configConnection struct {
	workerID uuid.UUID
	conn     *websocket.Conn
	writeMu  sync.Mutex
	mu       sync.Mutex
	pending  map[string]chan workerprotocol.ConfigRPCResponse
}

func (s *Server) workerConfigWS(c *gin.Context) {
	worker := currentWorker(c)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	state := &configConnection{workerID: worker.ID, conn: conn, pending: make(map[string]chan workerprotocol.ConfigRPCResponse)}
	s.configMu.Lock()
	if old := s.configConns[worker.ID]; old != nil {
		_ = old.conn.Close()
	}
	s.configConns[worker.ID] = state
	s.configMu.Unlock()
	defer func() {
		_ = conn.Close()
		s.configMu.Lock()
		if s.configConns[worker.ID] == state {
			delete(s.configConns, worker.ID)
		}
		s.configMu.Unlock()
		state.mu.Lock()
		for id, wait := range state.pending {
			close(wait)
			delete(state.pending, id)
		}
		state.mu.Unlock()
	}()
	for {
		_, payload, readErr := conn.ReadMessage()
		if readErr != nil {
			return
		}
		var response workerprotocol.ConfigRPCResponse
		if json.Unmarshal(payload, &response) != nil || response.ID == "" {
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

func (s *Server) callWorkerConfig(ctx context.Context, workerID uuid.UUID, method string, params any) (any, error) {
	s.configMu.RLock()
	state := s.configConns[workerID]
	s.configMu.RUnlock()
	if state == nil {
		return nil, errors.New("Worker 配置通道未连接")
	}
	id := uuid.NewString()
	wait := make(chan workerprotocol.ConfigRPCResponse, 1)
	state.mu.Lock()
	state.pending[id] = wait
	state.mu.Unlock()
	requestParams, _ := json.Marshal(params)
	request := workerprotocol.ConfigRPCRequest{ID: id, Method: method, Params: requestParams}
	payload, _ := json.Marshal(request)
	state.writeMu.Lock()
	writeErr := state.conn.WriteMessage(websocket.TextMessage, payload)
	state.writeMu.Unlock()
	if writeErr != nil {
		state.mu.Lock()
		delete(state.pending, id)
		state.mu.Unlock()
		return nil, writeErr
	}
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case response, ok := <-wait:
		if !ok {
			return nil, errors.New("Worker 配置通道已断开")
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
		return nil, errors.New("Worker 配置请求超时")
	}
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
		}
		problem(c, status, fmt.Sprintf("Worker 配置操作失败: %s", method), err)
		return
	}
	c.JSON(http.StatusOK, result)
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
		Revision       string                    `json:"revision"`
		ModelProvider  string                    `json:"modelProvider"`
		ModelProviders map[string]map[string]any `json:"modelProviders"`
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
