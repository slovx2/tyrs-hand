package appserverhub

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/slovx2/tyrs-hand/internal/codex"
)

// Tyrs Hand 保留的工具交给 Worker；具体工具授权仍由当前轮次的 Worker 绑定检查。
func workerOwnedTool(call codex.ToolCallRequest) bool {
	namespace := ""
	if call.Namespace != nil {
		namespace = *call.Namespace
	}
	switch namespace {
	case "github", "git", "tyrs_hand", "browser_files":
		return true
	case "":
		return call.Tool == "generate_image"
	default:
		return false
	}
}

type toolTurnOwner struct {
	source  *session
	desktop bool
}

type pendingToolTurn struct {
	owner toolTurnOwner
	done  chan struct{}
	state *toolThreadState
}

type toolThreadState struct {
	turns     map[string]toolTurnOwner
	pending   map[*pendingToolTurn]bool
	completed map[string]bool
}

// 调用方持有 Hub 锁。
func (r *Hub) toolThread(threadID string) *toolThreadState {
	state := r.toolThreads[threadID]
	if state == nil {
		state = &toolThreadState{turns: make(map[string]toolTurnOwner),
			pending: make(map[*pendingToolTurn]bool), completed: make(map[string]bool)}
		r.toolThreads[threadID] = state
	}
	return state
}

func (r *Hub) routeToolCall(ctx context.Context, request codex.ServerRequest) (any, error) {
	var call codex.ToolCallRequest
	if err := json.Unmarshal(request.Params, &call); err != nil {
		return nil, fmt.Errorf("解析动态工具请求: %w", err)
	}
	if call.ThreadID == "" || call.TurnID == "" || call.CallID == "" || call.Tool == "" {
		return nil, fmt.Errorf("动态工具缺少 thread、turn、call ID 或工具名称")
	}
	if workerOwnedTool(call) {
		worker := r.workerForThread(call.ThreadID)
		if worker == nil {
			return nil, fmt.Errorf("当前 Thread 没有活动的 Worker 工具执行器")
		}
		return worker.invoke(ctx, request)
	}
	owner, err := r.desktopToolOwner(ctx, call.ThreadID, call.TurnID)
	if err != nil {
		return codex.TextToolResult(err.Error(), false), nil
	}
	// 一次调用只交给一个执行者；失败、超时或断连均不换端重试。
	return owner.invoke(ctx, request)
}

func (r *Hub) desktopToolOwner(ctx context.Context, threadID, turnID string) (*session, error) {
	for {
		r.mu.Lock()
		var wait <-chan struct{}
		var owner toolTurnOwner
		var found bool
		if state := r.toolThreads[threadID]; state != nil {
			owner, found = state.turns[turnID]
			if !found && !state.completed[turnID] {
				for pending := range state.pending {
					wait = pending.done
					break
				}
			}
		}
		available := found && owner.desktop && owner.source != nil && r.sessions[owner.source.id] == owner.source
		r.mu.Unlock()
		if available {
			return owner.source, nil
		}
		if wait == nil {
			return nil, fmt.Errorf("桌面动态工具没有可用的所属 Desktop 连接，请在桌面重新打开该任务后重试")
		}
		// 工具请求可能早于 turn/start 的响应；等待精确的 turnId 绑定，不猜测所属连接。
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-r.done:
			return nil, errSessionClosed
		}
	}
}

func (r *Hub) bindDesktopTools(source *session, threadID string, result json.RawMessage) {
	if source.role != RoleDesktop {
		return
	}
	var response struct {
		Thread struct {
			Turns []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"turns"`
		} `json:"thread"`
	}
	if json.Unmarshal(result, &response) != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sessions[source.id] != source {
		return
	}
	state := r.toolThread(threadID)
	// 显式 resume 恢复已断开的 Desktop；旁观者不能抢占活动连接或 Worker 发起的轮次。
	for id, owner := range state.turns {
		if owner.desktop && owner.source == nil {
			owner.source = source
			state.turns[id] = owner
		}
	}
	for _, turn := range response.Thread.Turns {
		if turn.ID == "" || turn.Status != "inProgress" || state.completed[turn.ID] {
			continue
		}
		if _, exists := state.turns[turn.ID]; !exists && len(state.pending) == 0 {
			state.turns[turn.ID] = toolTurnOwner{source: source, desktop: true}
		}
	}
	r.releaseToolThread(threadID, state)
}

func (r *Hub) beginToolTurn(source *session, threadID string) *pendingToolTurn {
	if threadID == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.toolThread(threadID)
	pending := &pendingToolTurn{owner: toolTurnOwner{source: source, desktop: source.role == RoleDesktop},
		done: make(chan struct{}), state: state}
	state.pending[pending] = true
	return pending
}

func (r *Hub) finishToolTurnStart(threadID string, pending *pendingToolTurn, result json.RawMessage, err error) {
	if pending == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	state := pending.state
	delete(state.pending, pending)
	defer close(pending.done)
	if r.toolThreads[threadID] != state {
		return
	}
	_, turnID := threadScope(result)
	if err == nil && turnID != "" && !state.completed[turnID] {
		if _, exists := state.turns[turnID]; !exists {
			state.turns[turnID] = pending.owner
		}
	}
	if len(state.pending) == 0 {
		clear(state.completed)
	}
	r.releaseToolThread(threadID, state)
}

func (r *Hub) updateToolTurn(event codex.Event) {
	if event.Method != "turn/completed" && event.Method != "thread/archived" {
		return
	}
	threadID, turnID := threadScope(event.Params)
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.toolThreads[threadID]
	if state == nil {
		return
	}
	if event.Method == "thread/archived" {
		delete(r.toolThreads, threadID)
		return
	}
	delete(state.turns, turnID)
	if len(state.pending) > 0 {
		// 完成通知也可能早于启动响应，不能由迟到的响应重新创建已结束轮次。
		state.completed[turnID] = true
	}
	r.releaseToolThread(threadID, state)
}

// 以下两个方法的调用方持有 Hub 锁。
func (r *Hub) releaseToolThread(threadID string, state *toolThreadState) {
	if len(state.turns) == 0 && len(state.pending) == 0 {
		delete(r.toolThreads, threadID)
	}
}

func (r *Hub) unbindDesktopTools(source *session, threadID string) {
	for id, state := range r.toolThreads {
		if threadID != "" && threadID != id {
			continue
		}
		for turnID, owner := range state.turns {
			if owner.source == source {
				owner.source = nil
				state.turns[turnID] = owner
			}
		}
		for pending := range state.pending {
			if pending.owner.source == source {
				pending.owner.source = nil
			}
		}
	}
}
