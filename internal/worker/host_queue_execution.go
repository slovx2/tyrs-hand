package worker

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/slovx2/tyrs-hand/internal/appserverhub"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"go.uber.org/zap"
)

type hostQueueExecution struct {
	events     chan codex.Event
	ready      chan struct{}
	state      *desktopCallState
	controller *desktopController
	local      hostWorkspaceRuntime
	err        error
	closed     bool // 由 Controller.mu 保护。
}

func (c *HostDesktopController) startQueueExecutionLocked(threadID string, queue *hostQueueState, item *hostQueueItem, content json.RawMessage) {
	if item.execution != nil || c.runtime == nil {
		return
	}
	execution := &hostQueueExecution{events: make(chan codex.Event, 2048), ready: make(chan struct{})}
	item.execution = execution
	queue.turns[item.turnID] = execution
	// 冻结快照，迟到的 queue/update 响应不能改写已经执行的回合。
	snapshot := *item
	go c.runQueueExecution(threadID, &snapshot, execution, content)
}

func (c *HostDesktopController) runQueueExecution(threadID string, item *hostQueueItem, execution *hostQueueExecution, content json.RawMessage) {
	_, runtime := c.snapshot()
	entry, controller := item.journal, item.controller
	if entry == nil && c.processor.journals != nil {
		var err error
		entry, err = c.processor.journals.readQueue(threadID, item.ClientID)
		if err != nil {
			execution.err = err
			close(execution.ready)
			return
		}
		controller = c.queueController(entry, runtime)
	}
	if entry != nil {
		var err error
		entry, err = entry.withInput(content)
		if err != nil {
			execution.err = err
			close(execution.ready)
			return
		}
		entry.TurnID = item.turnID
		if err := c.processor.journals.saveQueue(entry); err != nil {
			execution.err = err
			close(execution.ready)
			return
		}
	}
	if controller == nil {
		var input struct {
			CWD string `json:"cwd"`
		}
		if entry != nil {
			_ = json.Unmarshal(entry.Params, &input)
		}
		if !filepath.IsAbs(input.CWD) {
			execution.err = errors.New("原生队列缺少入队时的工作目录与授权快照")
			close(execution.ready)
			return
		}
		execution.local = hostWorkspaceRuntime{Workspace: input.CWD, CodexHome: runtime.CodexHome(), ProjectKind: "directory"}
		if _, err := runHostGit(c.processor.workspaces.ctx, input.CWD, "rev-parse", "--show-toplevel"); err == nil {
			execution.local.ProjectKind = "git"
		}
		close(execution.ready)
		for event := range execution.events {
			if matched, _ := completedTurn(event.Params, threadID, item.turnID); event.Method == "turn/completed" && matched {
				if err := c.processor.journals.removeQueue(threadID, item.ClientID); err != nil {
					c.processor.logger.Warn("清理已完成本地队列记录失败", zap.Error(err))
				}
				break
			}
		}
		go cleanupBrowserTask(c.processor.cfg, c.processor.localBrowserTaskID(threadID, item.turnID), c.processor.browserScope())
		return
	}
	state, err := controller.prepareDesktopTurnState(entry.Params, execution.events, entry.Task)
	execution.state, execution.controller, execution.err = state, controller, err
	close(execution.ready)
	if err != nil {
		c.processor.logger.Error("原生队列回合 Journal 初始化失败", zap.String("thread_id", threadID), zap.String("turn_id", item.turnID), zap.Error(err))
		return
	}
	// 只观察官方实际启动的回合，不另发 turn/start，也不改写原生自动消费。
	result, _ := json.Marshal(map[string]any{"turn": map[string]string{"id": item.turnID}})
	controller.observeDesktopTurn(appserverhub.Call{Method: "thread/queue/add", Role: appserverhub.RoleDesktop, Params: entry.Params}, result, state)
	go cleanupBrowserTask(c.processor.cfg, state.task.Claimed.ID.String(), c.processor.browserScope())
	state.reporter.journal.mu.Lock()
	handedOff := state.reporter.desktopRequestPersisted
	state.reporter.journal.mu.Unlock()
	if !handedOff {
		c.processor.logger.Warn("保留队列身份，完整 Run Journal 尚未可靠落盘", zap.String("thread_id", threadID), zap.String("turn_id", item.turnID))
		return
	}
	if err := c.processor.journals.removeQueue(threadID, item.ClientID); err != nil {
		c.processor.logger.Warn("清理已转交 Run Journal 的队列记录失败", zap.Error(err))
	}
}

func (c *HostDesktopController) removeQueueJournalLocked(threadID string, item *hostQueueItem) {
	if c.processor.journals == nil || item.journal == nil {
		return
	}
	if err := c.processor.journals.removeQueue(threadID, item.ClientID); err != nil {
		c.processor.logger.Warn("清理已明确取消的队列记录失败", zap.Error(err))
	}
}

func (c *HostDesktopController) queueExecution(ctx context.Context, threadID, turnID string) (*hostQueueExecution, error) {
	if threadID == "" || turnID == "" {
		return nil, nil
	}
	for {
		c.mu.Lock()
		queue := c.queued[threadID]
		direct := c.active[threadID]
		if queue == nil || queue.foreign[turnID] || (direct != nil && direct.turnID == turnID) {
			c.mu.Unlock()
			return nil, nil
		}
		execution, changed := queue.turns[turnID], queue.changed
		c.mu.Unlock()
		if execution != nil {
			select {
			case <-execution.ready:
				return execution, execution.err
			case <-ctx.Done():
				return execution, ctx.Err()
			}
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// Runtime 先按真实 Thread/Turn 查队列；不能把下一队列回合交给上一普通回合的处理器。
func (c *HostDesktopController) HandleRuntimeRequest(ctx context.Context, request codex.ServerRequest) (bool, any, error) {
	threadID, turnID, _ := serverRequestScope(request.Params)
	execution, err := c.queueExecution(ctx, threadID, turnID)
	if err != nil {
		return true, nil, err
	}
	if execution == nil {
		return false, nil, nil
	}
	if request.Method == "item/tool/call" {
		var call codex.ToolCallRequest
		if err := json.Unmarshal(request.Params, &call); err != nil {
			return true, nil, err
		}
		if execution.state != nil {
			result, err := execution.state.toolHandler(ctx, call)
			return true, result, err
		}
		result, err := c.processor.handleLocalHostTool(ctx, execution.local, call)
		return true, result, err
	}
	if execution.state == nil {
		return true, nil, errors.New("队列入队时没有 Control 绑定，请在桌面端回答")
	}
	result, err := execution.state.interactiveHandler(ctx, request)
	return true, result, err
}
