package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

// 队列回合使用自身事件流；普通回合仍由 Runtime 绑定线程处理器。
func (c *desktopController) prepareDesktopTurnState(params json.RawMessage, events <-chan codex.Event, prepared *workerprotocol.Task) (*desktopCallState, error) {
	threadID, _ := callScope(params)
	client := c.workspace.currentClient()
	if client == nil {
		return nil, errors.New("宿主 Codex Runtime 正在恢复")
	}
	state := &desktopCallState{
		turnReady:   make(chan struct{}),
		events:      events,
		toolReady:   make(chan desktopToolRuntime, 1),
		interactive: make(chan bool, 1),
		commands:    make(chan workerprotocol.RunCommand, 16),
	}
	if events == nil {
		state.subscription = client.Subscribe(codex.ThreadFilter{ThreadID: threadID})
		state.events = state.subscription.Events()
	}
	state.unbind, state.unbindInput = func() {}, func() {}
	localTask, runtime, err := c.localDesktopTask(params)
	if err != nil {
		state.closeSubscription()
		return nil, err
	}
	if prepared != nil {
		localTask = *prepared
	}
	state.task = &localTask
	state.reporter, err = newDesktopEventReporter(c.processor.workspaces.ctx,
		c.processor, state.task)
	if err != nil {
		state.closeSubscription()
		return nil, fmt.Errorf("持久化 Desktop Run Journal: %w", err)
	}
	state.reporter.holdRegistration()
	state.reporter.journal.mu.Lock()
	state.reporter.journal.AppServerGeneration = c.workspace.currentGeneration()
	state.reporter.journal.mu.Unlock()
	if c.processor.coordinator != nil {
		c.processor.coordinator.register(state.reporter.journal, state.commands)
	}
	state.toolReady <- desktopToolRuntime{task: state.task, runtime: runtime,
		report: state.reporter.Report}
	state.toolHandler = func(ctx context.Context,
		request codex.ToolCallRequest,
	) (codex.ToolCallResult, error) {
		if err := state.requireTurn(ctx, request.ThreadID, request.TurnID); err != nil {
			return codex.ToolCallResult{}, err
		}
		select {
		case runtime := <-state.toolReady:
			state.toolReady <- runtime
			if runtime.err != nil {
				return codex.ToolCallResult{}, runtime.err
			}
			if !c.controlEnabled() && request.Namespace != nil &&
				(*request.Namespace == "tyrs_hand" || request.Tool == "publish_branch") {
				return codex.TextToolResult("Workspace 绑定已失效，Control 工具不可用", false), nil
			}
			if request.Namespace != nil && (*request.Namespace == "tyrs_hand" || request.Tool == "publish_branch") {
				// 定时任务等工具的状态只存在于 Control；断联时尽快返回工具错误，不能让 SSH 回合无限等待登记。
				waitTimeout := c.processor.cfg.ControlTimeout
				if waitTimeout <= 0 {
					waitTimeout = 30 * time.Second
				}
				waitCtx, cancel := context.WithTimeout(ctx, waitTimeout)
				err := state.reporter.waitControlRegistration(waitCtx)
				cancel()
				if err != nil {
					if ctx.Err() != nil {
						return codex.ToolCallResult{}, ctx.Err()
					}
					return codex.TextToolResult("Control 暂不可达，该工具需要 Control 在线后重试", false), nil
				}
			}
			return c.processor.handleRemoteHostDiscordTool(ctx, runtime.task,
				runtime.runtime, request)
		case <-ctx.Done():
			return codex.ToolCallResult{}, ctx.Err()
		case <-time.After(10 * time.Second):
			return codex.ToolCallResult{}, errors.New("动态工具尚未完成 Discord Control 绑定")
		}
	}
	state.interactiveHandler = func(ctx context.Context, request codex.ServerRequest) (any, error) {
		threadID, turnID, _ := serverRequestScope(request.Params)
		if err := state.requireTurn(ctx, threadID, turnID); err != nil {
			return nil, err
		}
		publishRemoteInteractiveState(state.interactive, true)
		defer publishRemoteInteractiveState(state.interactive, false)
		select {
		case runtime := <-state.toolReady:
			state.toolReady <- runtime
			if runtime.err != nil {
				return nil, runtime.err
			}
			if !c.controlEnabled() {
				return nil, errors.New("当前 Workspace 绑定已失效，请在桌面端回答")
			}
			if err := state.reporter.waitControlRegistration(ctx); err != nil {
				return nil, err
			}
			return c.processor.handleRemoteInteractive(ctx, runtime.task,
				c.workspace.currentGeneration(), request)
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Second):
			return nil, errors.New("desktop 交互尚未完成 Discord Control 绑定")
		}
	}
	if events == nil {
		state.unbind = c.workspace.bindTool(threadID, state.toolHandler)
		state.unbindInput = c.workspace.bindInteractive(threadID, state.interactiveHandler)
	}
	return state, nil
}

func (state *desktopCallState) closeSubscription() {
	if state.subscription != nil {
		state.subscription.Close()
	}
}

func (state *desktopCallState) releaseTurnWaiters() {
	state.turnReadyOnce.Do(func() {
		if state.turnReady != nil {
			close(state.turnReady)
		}
	})
}

func (state *desktopCallState) requireTurn(ctx context.Context, threadID, turnID string) error {
	select {
	case <-state.turnReady:
		if threadID == state.task.Claimed.ExternalThreadID && turnID != "" && turnID == state.task.Claimed.ConfirmedTurnID {
			return nil
		}
		return errors.New("工具或审批请求与当前回合身份不匹配")
	case <-ctx.Done():
		return ctx.Err()
	}
}
