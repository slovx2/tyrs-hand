package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"

	"github.com/slovx2/tyrs-hand/internal/appserverhub"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/participantidentity"
)

type hostCallState struct {
	turnID       string
	controller   *desktopController
	inner        any
	subscription *appserverhub.Subscription
	unbind       func()
	slot         *hostExecutionSlot
	queue        *hostQueueCall
	once         sync.Once
}

func (c *HostDesktopController) PrepareCall(ctx context.Context, call appserverhub.Call) (appserverhub.CallPlan, error) {
	integration, runtime := c.snapshot()
	threadID, _ := callScope(call.Params)
	// steer 必须沿用被操纵 turn 的身份，包括未绑定快照。
	if call.Method == "turn/steer" {
		var scope struct {
			ExpectedTurnID string `json:"expectedTurnId"`
		}
		if err := json.Unmarshal(call.Params, &scope); err != nil {
			return appserverhub.CallPlan{}, err
		}
		c.mu.Lock()
		if turn := c.active[threadID]; turn != nil {
			integration = turn.controller
		}
		var execution *hostQueueExecution
		if queue := c.queued[threadID]; queue != nil {
			execution = queue.turns[scope.ExpectedTurnID]
		}
		c.mu.Unlock()
		if execution != nil {
			select {
			case <-execution.ready:
				if execution.err != nil {
					return appserverhub.CallPlan{}, execution.err
				}
				integration = execution.controller
			case <-ctx.Done():
				return appserverhub.CallPlan{}, ctx.Err()
			}
		}
	}
	state := &hostCallState{controller: integration}
	var queueErr error
	state.queue, queueErr = c.prepareQueueCall(ctx, runtime, integration, call)
	if queueErr != nil {
		return appserverhub.CallPlan{}, queueErr
	}
	if state.queue != nil && state.queue.params != nil {
		call.Params = state.queue.params
	}
	if call.Method == "turn/start" && threadID != "" {
		if runtime == nil || runtime.Client() == nil {
			return appserverhub.CallPlan{}, errors.New("宿主 Codex Runtime 正在恢复")
		}
		// turn/start 可省略 cwd；从同一个官方 Thread 读取真实目录。
		var input map[string]json.RawMessage
		if err := json.Unmarshal(call.Params, &input); err != nil {
			return appserverhub.CallPlan{}, err
		}
		var cwd string
		_ = json.Unmarshal(input["cwd"], &cwd)
		if cwd == "" {
			var result struct {
				Thread struct {
					CWD string `json:"cwd"`
				} `json:"thread"`
			}
			if err := runtime.Client().Call(ctx, "thread/read", map[string]any{"threadId": threadID, "includeTurns": false}, &result); err != nil {
				return appserverhub.CallPlan{}, err
			}
			cwd = result.Thread.CWD
			input["cwd"], _ = json.Marshal(cwd)
			call.Params, _ = json.Marshal(input)
		}
		if !filepath.IsAbs(cwd) {
			return appserverhub.CallPlan{}, errors.New("桌面端 turn 工作目录必须是绝对路径")
		}
		state.subscription = runtime.Client().Subscribe(codex.ThreadFilter{ThreadID: threadID})
		c.mu.Lock()
		if c.active[threadID] != nil {
			c.mu.Unlock()
			state.subscription.Close()
			return appserverhub.CallPlan{}, errors.New("此 Thread 已有正在执行的 turn")
		}
		var slotErr error
		state.slot, slotErr = c.reserveExecutionSlot(threadID)
		if slotErr != nil {
			c.mu.Unlock()
			state.subscription.Close()
			return appserverhub.CallPlan{}, slotErr
		}
		c.active[threadID] = state
		c.mu.Unlock()
	}
	var plan appserverhub.CallPlan
	var err error
	if integration != nil {
		plan, err = integration.PrepareCall(ctx, call)
	} else {
		local := &desktopController{processor: c.processor}
		plan = appserverhub.CallPlan{Params: local.configureDesktopThreadRuntime(call, call.Params), Forward: true}
		if call.Method == "turn/start" || call.Method == "turn/steer" {
			plan.Params = participantidentity.StripTurnContext(plan.Params)
		}
		if call.Method == "thread/list" {
			plan.Params = desktopThreadListAllProviders(plan.Params)
		}
		if state.subscription != nil {
			var input struct {
				CWD string `json:"cwd"`
			}
			err = json.Unmarshal(call.Params, &input)
			if err == nil {
				localRuntime := hostWorkspaceRuntime{Workspace: filepath.Clean(input.CWD), CodexHome: runtime.CodexHome(), ProjectKind: "directory"}
				if _, gitErr := runHostGit(ctx, localRuntime.Workspace, "rev-parse", "--show-toplevel"); gitErr == nil {
					localRuntime.ProjectKind = "git"
				}
				state.unbind = runtime.BindTool(threadID, func(ctx context.Context, request codex.ToolCallRequest) (codex.ToolCallResult, error) {
					return c.processor.handleLocalHostTool(ctx, localRuntime, request)
				})
			}
		}
	}
	if err != nil {
		c.completeQueueCall(call, state.queue, nil, &codex.RequestError{State: codex.RequestNotSent, Cause: err})
		c.finishHostCall(threadID, state)
		return plan, err
	}
	state.inner, plan.State = plan.State, state
	return plan, nil
}

func (c *HostDesktopController) CompleteCall(ctx context.Context, call appserverhub.Call, plan appserverhub.CallPlan, result json.RawMessage, cause error) (json.RawMessage, error) {
	state, ok := plan.State.(*hostCallState)
	if !ok {
		return result, cause
	}
	c.completeQueueCall(call, state.queue, result, cause)
	if state.controller != nil {
		inner := plan
		inner.State = state.inner
		result, cause = state.controller.CompleteCall(ctx, call, inner, result, cause)
	}
	if state.subscription != nil {
		threadID, _ := callScope(plan.Params)
		_, turnID := callScope(result)
		c.mu.Lock()
		state.turnID = turnID
		if queue := c.queued[threadID]; queue != nil {
			c.notifyQueueChangedLocked(queue)
		}
		c.mu.Unlock()
		if cause != nil || turnID == "" {
			c.finishHostCall(threadID, state)
		} else {
			go c.observeHostCall(threadID, turnID, state)
		}
	}
	return result, cause
}

func (c *HostDesktopController) observeHostCall(threadID, turnID string, state *hostCallState) {
	taskID := c.processor.localBrowserTaskID(threadID, turnID)
	if bound, ok := state.inner.(*desktopCallState); ok {
		taskID = bound.task.Claimed.ID.String()
	}
	defer func() {
		// 本地授权和浏览器资源的结束不等待 Control 上传，也不阻塞下一 turn。
		c.finishHostCall(threadID, state)
		go cleanupBrowserTask(c.processor.cfg, taskID, c.processor.browserScope())
	}()
	for {
		select {
		case <-c.processor.workspaces.ctx.Done():
			return
		case event, ok := <-state.subscription.Events():
			if !ok {
				return
			}
			if event.Method == "turn/completed" {
				if matched, _ := completedTurn(event.Params, threadID, turnID); matched {
					return
				}
			}
		}
	}
}

func (c *HostDesktopController) finishHostCall(threadID string, state *hostCallState) {
	state.once.Do(func() {
		state.slot.release()
		if state.subscription != nil {
			state.subscription.Close()
		}
		if state.unbind != nil {
			state.unbind()
		}
		if bound, ok := state.inner.(*desktopCallState); ok {
			bound.unbind()
			bound.unbindInput()
		}
		c.mu.Lock()
		if c.active[threadID] == state {
			delete(c.active, threadID)
		}
		c.mu.Unlock()
	})
}

func (c *HostDesktopController) ResolveInteractive(ctx context.Context, request codex.ServerRequest, answer json.RawMessage, surface appserverhub.Role) (bool, json.RawMessage, error) {
	threadID, turnID, _ := serverRequestScope(request.Params)
	execution, err := c.queueExecution(ctx, threadID, turnID)
	if err != nil {
		return false, nil, err
	}
	if execution != nil && execution.controller != nil && execution.controller.controlEnabled() {
		return execution.controller.ResolveInteractive(ctx, request, answer, surface)
	}
	c.mu.Lock()
	state := c.active[threadID]
	c.mu.Unlock()
	if state != nil && state.controller != nil && state.controller.controlEnabled() {
		return state.controller.ResolveInteractive(ctx, request, answer, surface)
	}
	return true, answer, nil
}

func (c *HostDesktopController) ConfigureEphemeralThread(ctx context.Context, call appserverhub.Call) (json.RawMessage, error) {
	return (&desktopController{processor: c.processor}).ConfigureEphemeralThread(ctx, call)
}

func (c *HostDesktopController) WaitArchiveReady(ctx context.Context, call appserverhub.Call, plan appserverhub.CallPlan) error {
	state, ok := plan.State.(*hostCallState)
	if !ok || state.controller == nil {
		return nil
	}
	plan.State = state.inner
	return state.controller.WaitArchiveReady(ctx, call, plan)
}

func (p *Processor) localBrowserTaskID(threadID, turnID string) string {
	identity := p.runtimeIdentity
	digest := sha256.Sum256([]byte(identity.WorkerID + "\x00" + string(identity.Engine) + "\x00" + threadID + "\x00" + turnID))
	return "desktop-" + hex.EncodeToString(digest[:])
}

func (p *Processor) handleLocalHostTool(ctx context.Context, runtime hostWorkspaceRuntime, request codex.ToolCallRequest) (codex.ToolCallResult, error) {
	if request.ThreadID == "" || request.TurnID == "" || request.CallID == "" {
		return codex.ToolCallResult{}, errors.New("本地工具缺少 thread、turn 或 call ID")
	}
	namespace := ""
	if request.Namespace != nil {
		namespace = *request.Namespace
	}
	switch namespace {
	case "":
		if request.Tool == "generate_image" {
			return p.executeImageGenerationTool(ctx, runtime.Workspace, request), nil
		}
	case browserToolNamespace:
		return executeBrowserTool(ctx, p.cfg, p.localBrowserTaskID(request.ThreadID, request.TurnID), runtime.Workspace, request)
	case "git":
		if request.Tool == "publish_branch" {
			return codex.TextToolResult("Forum 发布需要有效的 Workspace 绑定", false), nil
		}
		return p.executeRemoteHostGit(ctx, runtime, request)
	case "tyrs_hand":
		return codex.TextToolResult("Control 自动任务需要有效的 Workspace 绑定", false), nil
	}
	return codex.ToolCallResult{}, errors.New("未知宿主动态工具")
}
