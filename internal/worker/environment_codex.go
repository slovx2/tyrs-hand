package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/codexrelay"
	"github.com/slovx2/tyrs-hand/internal/devcontainer"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

type environmentCodexRegistry struct {
	ctx       context.Context
	processor *RemoteProcessor

	mu      sync.Mutex
	entries map[uuid.UUID]*environmentCodex
}

type environmentCodex struct {
	relay      *codexrelay.Relay
	client     *codexrelay.Client
	manifest   workerprotocol.EnvironmentManifest
	runtime    devcontainer.Runtime
	generation int64

	mu                  sync.Mutex
	toolHandlers        map[string]toolBinding
	interactiveHandlers map[string]interactiveBinding
	nextBinding         uint64
}

type toolBinding struct {
	id      uint64
	handler codex.ToolHandler
}

type interactiveBinding struct {
	id      uint64
	handler codex.ServerRequestHandler
}

func newEnvironmentCodexRegistry(ctx context.Context, processor *RemoteProcessor) *environmentCodexRegistry {
	registry := &environmentCodexRegistry{ctx: ctx, processor: processor,
		entries: make(map[uuid.UUID]*environmentCodex)}
	go func() {
		<-ctx.Done()
		registry.close()
	}()
	return registry
}

func (r *environmentCodexRegistry) ensure(runtime devcontainer.Runtime,
	manifest *workerprotocol.EnvironmentManifest,
) (*environmentCodex, error) {
	if runtime.EnvironmentID == uuid.Nil || runtime.AppServerSocket == "" || runtime.RelaySocket == "" {
		return nil, errors.New("环境 Codex Relay 缺少环境或 Socket")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if current := r.entries[runtime.EnvironmentID]; current != nil {
		select {
		case <-current.client.Done():
			_ = current.relay.Close()
			delete(r.entries, runtime.EnvironmentID)
		default:
			if manifest != nil {
				current.mu.Lock()
				current.manifest = *manifest
				current.mu.Unlock()
			}
			return current, nil
		}
	}
	entry := &environmentCodex{runtime: runtime, generation: time.Now().UnixNano(),
		toolHandlers:        make(map[string]toolBinding),
		interactiveHandlers: make(map[string]interactiveBinding)}
	if manifest != nil {
		entry.manifest = *manifest
	}
	controller := &desktopRelayController{processor: r.processor, environment: entry}
	relay, err := codexrelay.Start(r.ctx, codexrelay.Options{
		SocketPath: runtime.RelaySocket, UpstreamSocketPath: runtime.AppServerSocket,
		Controller: controller,
	})
	if err != nil {
		return nil, fmt.Errorf("启动环境 Codex Relay: %w", err)
	}
	entry.relay = relay
	client, err := relay.OpenClient(codexrelay.ClientOptions{Role: codexrelay.RoleWorker,
		ServerRequestHandler: entry.handleServerRequest})
	if err != nil {
		_ = relay.Close()
		return nil, err
	}
	entry.client = client
	r.entries[runtime.EnvironmentID] = entry
	return entry, nil
}

func (e *environmentCodex) sshParticipant() (workerprotocol.ParticipantIdentity, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.manifest.SSHParticipant == nil {
		return workerprotocol.ParticipantIdentity{}, false
	}
	return *e.manifest.SSHParticipant, true
}

func (r *environmentCodexRegistry) retain(environmentIDs map[uuid.UUID]bool) {
	r.mu.Lock()
	removed := make([]*environmentCodex, 0)
	for environmentID, entry := range r.entries {
		if !environmentIDs[environmentID] {
			delete(r.entries, environmentID)
			removed = append(removed, entry)
		}
	}
	r.mu.Unlock()
	for _, entry := range removed {
		_ = entry.client.Close()
		_ = entry.relay.Close()
	}
}

func (r *environmentCodexRegistry) idle(environmentID uuid.UUID) bool {
	r.mu.Lock()
	entry := r.entries[environmentID]
	r.mu.Unlock()
	if entry == nil {
		return true
	}
	entry.mu.Lock()
	toolsIdle := len(entry.toolHandlers) == 0
	entry.mu.Unlock()
	return toolsIdle && entry.relay.Stats().DesktopConnections == 0
}

func (e *environmentCodex) bindTool(threadID string, handler codex.ToolHandler) func() {
	e.mu.Lock()
	e.nextBinding++
	binding := toolBinding{id: e.nextBinding, handler: handler}
	e.toolHandlers[threadID] = binding
	e.mu.Unlock()
	return func() {
		e.mu.Lock()
		if current, ok := e.toolHandlers[threadID]; ok && current.id == binding.id {
			delete(e.toolHandlers, threadID)
		}
		e.mu.Unlock()
	}
}

func (e *environmentCodex) bindInteractive(threadID string,
	handler codex.ServerRequestHandler,
) func() {
	e.mu.Lock()
	e.nextBinding++
	binding := interactiveBinding{id: e.nextBinding, handler: handler}
	e.interactiveHandlers[threadID] = binding
	e.mu.Unlock()
	return func() {
		e.mu.Lock()
		if current, ok := e.interactiveHandlers[threadID]; ok && current.id == binding.id {
			delete(e.interactiveHandlers, threadID)
		}
		e.mu.Unlock()
	}
}

func (e *environmentCodex) handleServerRequest(ctx context.Context,
	request codex.ServerRequest,
) (any, error) {
	threadID, _, _ := serverRequestScope(request.Params)
	switch request.Method {
	case "item/tool/call":
		var call codex.ToolCallRequest
		if err := json.Unmarshal(request.Params, &call); err != nil {
			return nil, fmt.Errorf("解析动态工具请求: %w", err)
		}
		e.mu.Lock()
		handler := e.toolHandlers[call.ThreadID].handler
		e.mu.Unlock()
		if handler == nil {
			return nil, errors.New("当前 Thread 没有活动的工具授权")
		}
		return handler(ctx, call)
	case "item/tool/requestUserInput":
		e.mu.Lock()
		handler := e.interactiveHandlers[threadID].handler
		e.mu.Unlock()
		if handler == nil {
			return nil, errors.New("当前 Thread 没有活动的 requestUserInput 控制器")
		}
		return handler(ctx, request)
	default:
		return nil, fmt.Errorf("Worker尚未支持 Codex Server Request %q", request.Method)
	}
}

func serverRequestScope(raw json.RawMessage) (string, string, string) {
	var value struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		ItemID   string `json:"itemId"`
	}
	_ = json.Unmarshal(raw, &value)
	return value.ThreadID, value.TurnID, value.ItemID
}

func (r *environmentCodexRegistry) close() {
	r.mu.Lock()
	entries := make([]*environmentCodex, 0, len(r.entries))
	for _, entry := range r.entries {
		entries = append(entries, entry)
	}
	r.entries = make(map[uuid.UUID]*environmentCodex)
	r.mu.Unlock()
	for _, entry := range entries {
		_ = entry.client.Close()
		_ = entry.relay.Close()
	}
}
