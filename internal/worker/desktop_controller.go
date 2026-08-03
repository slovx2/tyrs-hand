package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/appserverhub"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/participantidentity"
	"github.com/slovx2/tyrs-hand/internal/ports"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"go.uber.org/zap"
)

type desktopController struct {
	processor *Processor
	workspace *workspaceCodex
}

type desktopCallState struct {
	subscription *appserverhub.Subscription
	toolReady    chan desktopToolRuntime
	unbind       func()
	unbindInput  func()
}

type desktopLifecycleCallState struct {
	request workerprotocol.ThreadLifecycleState
}

type desktopThreadCallState struct {
	request workerprotocol.DesktopThreadState
}

type desktopRollbackCallState struct {
	request workerprotocol.DesktopRollbackState
}

type desktopToolRuntime struct {
	task    *workerprotocol.Task
	runtime hostWorkspaceRuntime
	report  func(string, json.RawMessage)
	err     error
}

func (c *desktopController) PrepareCall(ctx context.Context,
	call appserverhub.Call,
) (appserverhub.CallPlan, error) {
	plan := appserverhub.CallPlan{Params: append(json.RawMessage(nil), call.Params...), Forward: true}
	if call.Method == "turn/start" || call.Method == "turn/steer" {
		if identity, ok := c.workspace.ownerParticipant(); ok {
			plan.Params = participantidentity.InjectTurnContext(plan.Params,
				participantidentity.Participant{
					ID: identity.ParticipantID, DisplayName: identity.DisplayName,
				})
		} else {
			plan.Params = participantidentity.StripTurnContext(plan.Params)
		}
	}
	plan.Params = c.configureDesktopThreadRuntime(call, plan.Params)
	switch call.Method {
	case "thread/list":
		if call.Role == appserverhub.RoleDesktop {
			plan.Params = desktopThreadListAllProviders(call.Params)
		}
	case "thread/start":
		if call.Role == appserverhub.RoleDesktop {
			state, err := c.prepareDesktopThread(ctx, call)
			if err != nil {
				return plan, err
			}
			plan.State = &desktopThreadCallState{request: state}
		}
	case "thread/fork":
		if call.Role == appserverhub.RoleDesktop {
			state, err := c.prepareDesktopThread(ctx, call)
			if err != nil {
				return plan, err
			}
			plan.State = &desktopThreadCallState{request: state}
		}
	case "turn/start":
		if c.processor != nil && c.processor.client != nil {
			requestCtx, cancel := context.WithTimeout(ctx, c.processor.cfg.ControlTimeout)
			preflight, err := c.processor.client.PreflightDesktopTurn(requestCtx,
				workerprotocol.DesktopTurnPreflightRequest{
					WorkspaceID: c.workspace.runtime.WorkspaceID, Params: plan.Params,
				})
			cancel()
			if err != nil {
				return plan, err
			}
			if len(preflight.Params) > 0 {
				plan.Params = preflight.Params
			}
		}
		threadID, _ := callScope(plan.Params)
		if threadID == "" {
			return plan, nil
		}
		state := &desktopCallState{
			subscription: c.workspace.client.Subscribe(codex.ThreadFilter{ThreadID: threadID}),
			toolReady:    make(chan desktopToolRuntime, 1),
		}
		state.unbind = c.workspace.bindTool(threadID, func(ctx context.Context,
			request codex.ToolCallRequest,
		) (codex.ToolCallResult, error) {
			select {
			case runtime := <-state.toolReady:
				state.toolReady <- runtime
				if runtime.err != nil {
					return codex.ToolCallResult{}, runtime.err
				}
				return c.processor.handleRemoteHostDiscordTool(ctx, runtime.task,
					runtime.runtime, request, runtime.report)
			case <-ctx.Done():
				return codex.ToolCallResult{}, ctx.Err()
			case <-time.After(10 * time.Second):
				return codex.ToolCallResult{}, errors.New("动态工具尚未完成 Discord Control 绑定")
			}
		})
		state.unbindInput = c.workspace.bindInteractive(threadID,
			func(ctx context.Context, request codex.ServerRequest) (any, error) {
				select {
				case runtime := <-state.toolReady:
					state.toolReady <- runtime
					if runtime.err != nil {
						return nil, runtime.err
					}
					return c.processor.handleRemoteInteractive(ctx, runtime.task,
						c.workspace.generation, request)
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(10 * time.Second):
					return nil, errors.New("desktop 交互尚未完成 Discord Control 绑定")
				}
			})
		plan.State = state
	case "thread/rollback":
		request := workerprotocol.DesktopRollbackPrepareRequest{
			WorkspaceID: c.workspace.runtime.WorkspaceID,
			RequestKey:  desktopRequestKey(call.Method, plan.Params, nil), Params: plan.Params,
		}
		var state workerprotocol.DesktopRollbackState
		var err error
		for attempt := 0; attempt < 8; attempt++ {
			requestCtx, cancel := context.WithTimeout(ctx, c.processor.cfg.ControlTimeout)
			state, err = c.processor.client.PrepareDesktopRollback(requestCtx, request)
			cancel()
			if err == nil || !strings.Contains(err.Error(), "409 Conflict") || attempt == 7 {
				break
			}
			if !waitContext(ctx, 300*time.Millisecond) {
				break
			}
		}
		if err != nil {
			return plan, err
		}
		plan.State = &desktopRollbackCallState{request: state}
	case "thread/archive", "thread/unarchive":
		threadID, _ := callScope(plan.Params)
		if threadID == "" {
			return plan, nil
		}
		desiredState := "archived"
		if call.Method == "thread/unarchive" {
			desiredState = "active"
		}
		requestCtx, cancel := context.WithTimeout(c.processor.workspaces.ctx,
			c.processor.cfg.ControlTimeout)
		state, err := c.processor.client.PrepareDesktopThreadLifecycle(requestCtx,
			workerprotocol.ThreadLifecyclePrepareRequest{
				WorkspaceID: c.workspace.runtime.WorkspaceID,
				ThreadID:    threadID, DesiredState: desiredState,
			})
		cancel()
		if err != nil {
			c.processor.logger.Warn("登记 Desktop Thread lifecycle 失败，继续执行官方操作",
				zap.String("thread_id", threadID), zap.Error(err))
			return plan, nil
		}
		if len(state.Response) > 0 && string(state.Response) != "null" {
			plan.Forward = false
			plan.Result = append(json.RawMessage(nil), state.Response...)
		}
		plan.State = &desktopLifecycleCallState{request: state}
	}
	return plan, nil
}

func (c *desktopController) CompleteCall(_ context.Context, call appserverhub.Call,
	plan appserverhub.CallPlan, result json.RawMessage, cause error,
) (json.RawMessage, error) {
	if lifecycle, ok := plan.State.(*desktopLifecycleCallState); ok {
		go c.completeDesktopLifecycle(lifecycle.request, result, cause)
	}
	if rollback, ok := plan.State.(*desktopRollbackCallState); ok {
		var requestErr *codex.RequestError
		if cause != nil && errors.As(cause, &requestErr) && requestErr.State == codex.RequestUnknown {
			runtime := codex.NewRuntime(c.workspace.client)
			if snapshot, err := runtime.ReadThread(c.processor.workspaces.ctx,
				rollback.request.ThreadID); err == nil {
				if _, exists := snapshot.TurnByID(rollback.request.TargetTurnID); !exists {
					cause = nil
					result = json.RawMessage(`{}`)
				}
			}
		}
		request := workerprotocol.DesktopRollbackCompleteRequest{
			WorkspaceID: rollback.request.WorkspaceID, Response: result,
		}
		if cause != nil {
			request.Error = cause.Error()
		}
		ctx, cancel := context.WithTimeout(c.processor.workspaces.ctx,
			c.processor.cfg.ControlTimeout)
		err := c.processor.client.CompleteDesktopRollback(ctx, rollback.request.ID, request)
		cancel()
		if err != nil {
			return result, err
		}
	}
	if cause != nil {
		var requestErr *codex.RequestError
		responseUnknown := errors.As(cause, &requestErr) && requestErr.State == codex.RequestUnknown
		if call.Method == "turn/start" && !responseUnknown {
			var params struct {
				ClientUserMessageID string `json:"clientUserMessageId"`
			}
			_ = json.Unmarshal(plan.Params, &params)
			if reservationID, err := uuid.Parse(params.ClientUserMessageID); err == nil {
				ctx, cancel := context.WithTimeout(c.processor.workspaces.ctx,
					c.processor.cfg.ControlTimeout)
				_ = c.processor.client.CompleteDesktopRollback(ctx, reservationID,
					workerprotocol.DesktopRollbackCompleteRequest{
						WorkspaceID: c.workspace.runtime.WorkspaceID, Error: cause.Error(),
					})
				cancel()
			}
		}
		c.cleanupDesktopCall(plan, cause)
		return result, cause
	}
	if state, ok := plan.State.(*desktopThreadCallState); ok {
		go c.completeDesktopThread(state.request, result)
	}
	switch call.Method {
	case "thread/start", "thread/fork":
		if threadID, name := desktopThreadName(result); threadID != "" && name != "" {
			go c.workspace.recordThreadName(c.processor.workspaces.ctx, threadID, name)
		}
		if threadID, model, effort, tier := desktopThreadRuntime(result); threadID != "" {
			go c.workspace.recordThreadSettings(c.processor.workspaces.ctx, threadID,
				model, effort, tier, "", "desktop")
		}
	case "thread/resume":
		if threadID, name := desktopThreadName(result); threadID != "" && name != "" {
			go c.workspace.recordThreadName(c.processor.workspaces.ctx, threadID, name)
		}
		if threadID, model, effort, tier := desktopThreadRuntime(result); threadID != "" {
			go c.workspace.recordThreadSettings(c.processor.workspaces.ctx, threadID,
				model, effort, tier, "", "desktop")
		}
	case "turn/start":
		state, _ := plan.State.(*desktopCallState)
		if state != nil {
			call.Params = plan.Params
			go c.observeDesktopTurn(call, result, state)
		}
	case "turn/steer":
		go c.observeDesktopSteer(call, result)
	}
	return result, nil
}

func (c *desktopController) completeDesktopLifecycle(
	state workerprotocol.ThreadLifecycleState, result json.RawMessage, cause error,
) {
	if state.ID == uuid.Nil {
		return
	}
	request := workerprotocol.ThreadLifecycleCompleteRequest{
		WorkspaceID: state.WorkspaceID, Response: result,
	}
	if cause != nil {
		request.Error = cause.Error()
	}
	ctx := c.processor.workspaces.ctx
	for attempt := 0; attempt < 8 && ctx.Err() == nil; attempt++ {
		requestCtx, cancel := context.WithTimeout(ctx, c.processor.cfg.ControlTimeout)
		err := c.processor.client.CompleteThreadLifecycle(requestCtx, state.ID, request)
		cancel()
		if err == nil {
			return
		}
		c.processor.logger.Warn("提交 Desktop Thread lifecycle 结果失败",
			zap.String("thread_id", state.ThreadID), zap.Error(err))
		if !waitContext(ctx, 500*time.Millisecond) {
			return
		}
	}
}

func (c *desktopController) WaitArchiveReady(ctx context.Context, _ appserverhub.Call,
	plan appserverhub.CallPlan,
) error {
	state, ok := plan.State.(*desktopLifecycleCallState)
	if !ok || state.request.ID == uuid.Nil {
		return nil
	}
	for {
		requestCtx, cancel := context.WithTimeout(ctx, c.processor.cfg.ControlTimeout)
		current, err := c.processor.client.ThreadLifecycleState(requestCtx, state.request.ID)
		cancel()
		if err != nil {
			return err
		}
		switch current.Status {
		case "applying", "completed":
			return nil
		case "failed", "canceled":
			if current.Error != "" {
				return errors.New(current.Error)
			}
			return errors.New("desktop Thread 归档请求已取消")
		}
		if !waitContext(ctx, 250*time.Millisecond) {
			return ctx.Err()
		}
	}
}

func desktopThreadName(raw json.RawMessage) (string, string) {
	var value struct {
		Thread struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"thread"`
	}
	_ = json.Unmarshal(raw, &value)
	return value.Thread.ID, strings.TrimSpace(value.Thread.Name)
}

func desktopThreadRuntime(raw json.RawMessage) (string, string, string, string) {
	var value struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		Model           string `json:"model"`
		ReasoningEffort string `json:"reasoningEffort"`
		ServiceTier     string `json:"serviceTier"`
	}
	_ = json.Unmarshal(raw, &value)
	return value.Thread.ID, strings.TrimSpace(value.Model),
		strings.TrimSpace(value.ReasoningEffort), strings.TrimSpace(value.ServiceTier)
}

func desktopThreadListAllProviders(params json.RawMessage) json.RawMessage {
	var value map[string]any
	if json.Unmarshal(params, &value) != nil {
		return params
	}
	if value == nil {
		value = make(map[string]any)
	}
	// 远程环境的 Thread 可能由不同 Provider 创建，桌面端必须查询全部 Provider。
	value["modelProviders"] = []string{}
	result, err := json.Marshal(value)
	if err != nil {
		return params
	}
	return result
}

func (c *desktopController) ConfigureEphemeralThread(_ context.Context,
	call appserverhub.Call,
) (json.RawMessage, error) {
	switch call.Method {
	case "thread/start", "thread/fork":
		// 标题、描述等临时 Thread 必须沿用当前环境的 Provider，
		// 但不应改变桌面端为该任务设置的工具边界或进入 Discord Control。
		return c.injectDesktopRuntime(call.Params, desktopRuntimeInjection{}), nil
	default:
		return append(json.RawMessage(nil), call.Params...), nil
	}
}

func (c *desktopController) ResolveInteractive(ctx context.Context,
	request codex.ServerRequest, answer json.RawMessage, surface appserverhub.Role,
) (bool, json.RawMessage, error) {
	if surface != appserverhub.RoleDesktop {
		return true, answer, nil
	}
	threadID, turnID, itemID := serverRequestScope(request.Params)
	input := workerprotocol.InteractiveAnswerRequest{
		WorkspaceID: c.workspace.runtime.WorkspaceID, ThreadID: threadID,
		TurnID: turnID, ItemID: itemID, Surface: "desktop", Answer: answer,
	}
	state, err := c.answerDesktopInteractive(ctx, input)
	if err != nil {
		// Control 不可用不能让用户刚刚提交的 Desktop 答案失效；后台继续补记仲裁结果。
		go c.compensateDesktopInteractive(input)
		return true, answer, nil
	}
	if !state.Accepted {
		return false, nil, nil
	}
	for !state.Ready {
		if !waitContext(ctx, 250*time.Millisecond) {
			return false, nil, ctx.Err()
		}
		state, err = c.processor.client.InteractiveState(ctx, state.ID)
		if err != nil {
			return false, nil, err
		}
	}
	return true, state.Answer, nil
}

type desktopRuntimeInjection struct {
	includeBrowserMCP   bool
	includeDynamicTools bool
}

func (c *desktopController) configureDesktopThreadRuntime(call appserverhub.Call,
	params json.RawMessage,
) json.RawMessage {
	if call.Role != appserverhub.RoleDesktop {
		return params
	}
	var options desktopRuntimeInjection
	switch call.Method {
	case "thread/start":
		options.includeBrowserMCP = true
		options.includeDynamicTools = true
	case "thread/fork", "thread/resume":
		options.includeBrowserMCP = true
	default:
		return params
	}
	params = c.injectDesktopRuntime(params, options)
	return participantidentity.AppendDeveloperInstructions(params)
}

func (c *desktopController) injectDesktopRuntime(params json.RawMessage,
	options desktopRuntimeInjection,
) json.RawMessage {
	var value map[string]any
	if json.Unmarshal(params, &value) != nil {
		return params
	}
	config, _ := value["config"].(map[string]any)
	if config == nil {
		config = make(map[string]any)
	}
	if effort, ok := value["effort"].(string); ok && strings.TrimSpace(effort) != "" {
		config["model_reasoning_effort"] = strings.TrimSpace(effort)
	}
	if tier, ok := value["serviceTier"].(string); ok && strings.TrimSpace(tier) != "" {
		config["service_tier"] = strings.TrimSpace(tier)
	}
	delete(value, "effort")
	delete(value, "serviceTier")
	if options.includeBrowserMCP {
		applyBrowserMCPConfig(config, c.processor.cfg)
	}
	hideManagedSecrets(config)
	value["config"] = config
	if options.includeDynamicTools {
		cwd, _ := value["cwd"].(string)
		allowPublish := c.desktopWorkspaceAllowsPublish(cwd)
		specs := withBrowserTools(c.processor.cfg, localGitSpec(allowPublish))
		current, _ := value["dynamicTools"].([]any)
		for _, spec := range specs {
			encoded, _ := json.Marshal(spec)
			var item any
			_ = json.Unmarshal(encoded, &item)
			current = append(current, item)
		}
		value["dynamicTools"] = current
	}
	result, err := json.Marshal(value)
	if err != nil {
		return params
	}
	return result
}

func (c *desktopController) desktopWorkspaceAllowsPublish(cwd string) bool {
	c.workspace.mu.Lock()
	forums := append([]workerprotocol.WorkspaceForum(nil), c.workspace.manifest.Forums...)
	hostRuntime := c.workspace.hostRuntime
	c.workspace.mu.Unlock()
	if hostRuntime == nil {
		return false
	}
	for _, forum := range forums {
		workspace, err := hostWorkspacePath(hostRuntime.WorkspaceRoot(), forum.WorkspaceRelative)
		if err == nil && workspace == cwd {
			return forum.WorkspaceKind == "git"
		}
	}
	return false
}

func (c *desktopController) prepareDesktopThread(ctx context.Context,
	call appserverhub.Call,
) (workerprotocol.DesktopThreadState, error) {
	workspaceRoot := ""
	if c.workspace.hostRuntime != nil {
		workspaceRoot = c.workspace.hostRuntime.WorkspaceRoot()
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.controlTimeout())
	defer cancel()
	return c.processor.client.PrepareDesktopThread(requestCtx,
		workerprotocol.DesktopThreadPrepareRequest{
			WorkspaceID:   c.workspace.runtime.WorkspaceID,
			WorkspaceRoot: workspaceRoot,
			Operation:     strings.TrimPrefix(call.Method, "thread/"),
			RequestKey: desktopRequestKey(call.Method, call.Params,
				json.RawMessage(uuid.NewString())),
			Params: call.Params,
		})
}

func hostWorkspacePath(root, relative string) (string, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	relative = filepath.Clean(strings.TrimSpace(relative))
	if !filepath.IsAbs(root) || relative == "." || filepath.IsAbs(relative) ||
		relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", errors.New("宿主 Workspace 相对路径无效")
	}
	return filepath.Join(root, relative), nil
}

func (c *desktopController) completeDesktopThread(
	state workerprotocol.DesktopThreadState, result json.RawMessage,
) {
	if state.ID == uuid.Nil {
		return
	}
	ctx := c.processor.workspaces.ctx
	for attempt := 0; attempt < 8 && ctx.Err() == nil; attempt++ {
		requestCtx, cancel := context.WithTimeout(ctx, c.controlTimeout())
		_, err := c.processor.client.CompleteDesktopThread(requestCtx, state.ID,
			workerprotocol.DesktopThreadCompleteRequest{
				WorkspaceID: c.workspace.runtime.WorkspaceID, Response: result,
			})
		cancel()
		if err == nil {
			return
		}
		c.processor.logger.Warn("提交 Desktop Thread 绑定失败",
			zap.String("request_id", state.ID.String()), zap.Error(err))
		if !retryableDesktopControlError(err) || !waitContext(ctx, 500*time.Millisecond) {
			return
		}
	}
}

func (c *desktopController) failDesktopThread(
	state workerprotocol.DesktopThreadState, cause error,
) {
	if state.ID == uuid.Nil || cause == nil {
		return
	}
	ctx := c.processor.workspaces.ctx
	for attempt := 0; attempt < 8 && ctx.Err() == nil; attempt++ {
		requestCtx, cancel := context.WithTimeout(ctx, c.controlTimeout())
		err := c.processor.client.FailDesktopThread(requestCtx, state.ID,
			workerprotocol.DesktopThreadFailRequest{
				WorkspaceID: c.workspace.runtime.WorkspaceID, Error: cause.Error(),
			})
		cancel()
		if err == nil {
			return
		}
		c.processor.logger.Warn("提交 Desktop Thread 失败状态失败",
			zap.String("request_id", state.ID.String()), zap.Error(err))
		if !retryableDesktopControlError(err) || !waitContext(ctx, 500*time.Millisecond) {
			return
		}
	}
}

func (c *desktopController) controlTimeout() time.Duration {
	if c.processor.cfg.ControlTimeout > 0 {
		return c.processor.cfg.ControlTimeout
	}
	return time.Minute
}

func retryableDesktopControlError(err error) bool {
	var response *workerprotocol.HTTPError
	if !errors.As(err, &response) {
		return true
	}
	return response.StatusCode == http.StatusRequestTimeout ||
		response.StatusCode == http.StatusTooEarly ||
		response.StatusCode == http.StatusTooManyRequests ||
		response.StatusCode >= http.StatusInternalServerError
}

func (c *desktopController) observeDesktopTurn(call appserverhub.Call,
	result json.RawMessage, state *desktopCallState,
) {
	defer state.subscription.Close()
	defer state.unbind()
	defer state.unbindInput()
	threadID, _ := callScope(call.Params)
	_, turnID := callScope(result)
	if threadID == "" || turnID == "" {
		state.toolReady <- desktopToolRuntime{err: errors.New("turn/start 响应缺少 Codex Turn ID")}
		return
	}
	ctx := c.processor.workspaces.ctx
	requestKey := desktopRequestKey(call.Method, call.Params, result)
	images, imageNotice, imageErr := desktopImagesFromTurn(ctx, call.Params,
		c.openDesktopImage)
	if imageErr != nil {
		c.processor.logger.Warn("读取 Desktop 图片失败，继续投影文本",
			zap.String("request_key", requestKey), zap.Error(imageErr))
	}
	var task workerprotocol.Task
	for ctx.Err() == nil {
		requestCtx, cancel := context.WithTimeout(ctx, c.processor.cfg.ControlTimeout)
		var err error
		task, err = c.processor.client.PrepareDesktopTurn(requestCtx,
			workerprotocol.DesktopTurnPrepareRequest{
				WorkspaceID: c.workspace.runtime.WorkspaceID,
				RequestKey:  requestKey,
				Params:      call.Params, Images: images, ImageError: imageNotice,
			})
		cancel()
		if err == nil {
			break
		}
		c.processor.logger.Warn("异步登记 Desktop Turn 失败，Desktop Turn 继续运行",
			zap.String("turn_id", turnID), zap.Error(err))
		if !waitContext(ctx, 500*time.Millisecond) {
			state.toolReady <- desktopToolRuntime{err: ctx.Err()}
			return
		}
	}
	if len(images) > 0 {
		taskCopy := task
		imagesCopy := append([]workerprotocol.DesktopImage(nil), images...)
		go c.syncDesktopImages(&taskCopy, imagesCopy)
	}
	reporter := newDesktopEventReporter(ctx, c.processor, &task)
	toolRuntime, runtimeErr := desktopRuntimeForTask(c.workspace.hostRuntime.WorkspaceRoot(),
		c.workspace.runtime.WorkspaceID, &task)
	state.toolReady <- desktopToolRuntime{
		task: &task, runtime: toolRuntime, report: reporter.Report, err: runtimeErr,
	}
	reporter.Report("discord.progress", remoteEventPayload(map[string]string{
		"state": "running", "detail": "Codex Desktop 正在处理请求。",
	}))
	if err := c.processor.client.RecordSubmission(ctx, &task, turnID); err != nil {
		c.finishDesktopTurn(ctx, &task, reporter, codexcontrol.TurnResult{}, err)
		return
	}
	if err := c.processor.client.ConfirmTurn(ctx, &task, turnID); err != nil {
		c.finishDesktopTurn(ctx, &task, reporter, codexcontrol.TurnResult{}, err)
		return
	}
	commands := make(chan workerprotocol.RunCommand, 16)
	heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
	go c.desktopTurnHeartbeat(heartbeatCtx, &task, commands)
	runtime := codex.NewRuntime(c.workspace.client)
	resultValue, err := c.processor.waitRemoteTurn(ctx, runtime, state.subscription.Events(),
		&task, threadID, turnID, commands,
		c.processor.hostDiscordCommandHandler(&task, toolRuntime, []ports.SkillRef{}, reporter.Report),
		remoteDiscordEventReporter(reporter.Report))
	cancelHeartbeat()
	if err == nil {
		reporter.Report("discord.progress", remoteEventPayload(map[string]string{
			"state": "completed", "detail": "本轮处理完成。",
		}))
	}
	c.finishDesktopTurn(ctx, &task, reporter, resultValue, err)
}

type desktopImageOpener func(context.Context, string) (io.ReadCloser, int64, error)

func desktopImagesFromTurn(ctx context.Context, params json.RawMessage,
	open desktopImageOpener,
) ([]workerprotocol.DesktopImage, string, error) {
	var value struct {
		Input []struct {
			Type string `json:"type"`
			Path string `json:"path"`
		} `json:"input"`
	}
	if err := json.Unmarshal(params, &value); err != nil {
		return nil, "", err
	}
	images := make([]workerprotocol.DesktopImage, 0)
	seen := make(map[string]struct{})
	total := int64(0)
	skipped := 0
	for _, item := range value.Input {
		path := filepath.Clean(strings.TrimSpace(item.Path))
		if item.Type != "localImage" || path == "." {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		if len(images) >= workerprotocol.DesktopImageCountLimit {
			skipped++
			continue
		}
		seen[path] = struct{}{}
		filename := filepath.Base(path)
		image := workerprotocol.DesktopImage{Filename: filename}
		file, size, err := open(ctx, path)
		if err != nil || size <= 0 || size > workerprotocol.DesktopImageFileLimit ||
			total+size > workerprotocol.DesktopImageTotalLimit {
			if file != nil {
				_ = file.Close()
			}
			image.Error = "文件不存在、不是普通文件或超过大小限制"
			images = append(images, image)
			continue
		}
		header := make([]byte, 512)
		n, readErr := file.Read(header)
		digest := sha256.New()
		_, _ = digest.Write(header[:n])
		copied, copyErr := io.Copy(digest, io.LimitReader(file,
			workerprotocol.DesktopImageFileLimit+1-int64(n)))
		closeErr := file.Close()
		if (readErr != nil && !errors.Is(readErr, io.EOF)) || copyErr != nil || closeErr != nil ||
			int64(n)+copied != size {
			image.Error = "读取文件失败"
			images = append(images, image)
			continue
		}
		mediaType := http.DetectContentType(header[:n])
		if !desktopImageTypeMatches(filename, mediaType) {
			image.Error = "扩展名与图片内容不匹配"
			images = append(images, image)
			continue
		}
		image.MediaType, image.Size = mediaType, size
		image.SHA256, image.SourcePath = fmt.Sprintf("%x", digest.Sum(nil)), path
		images = append(images, image)
		total += size
	}
	notice := ""
	if skipped > 0 {
		notice = fmt.Sprintf("另有 %d 张图片超过 %d 张限制", skipped,
			workerprotocol.DesktopImageCountLimit)
	}
	return images, notice, nil
}

func openLocalDesktopImage(_ context.Context, source string) (io.ReadCloser, int64, error) {
	path := filepath.Clean(strings.TrimSpace(source))
	info, err := os.Lstat(path)
	if err != nil || !filepath.IsAbs(path) || !info.Mode().IsRegular() {
		return nil, 0, errors.New("图片不是本地普通文件")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	return file, info.Size(), nil
}

func (c *desktopController) openDesktopImage(ctx context.Context,
	source string,
) (io.ReadCloser, int64, error) {
	return openLocalDesktopImage(ctx, source)
}

func desktopImageTypeMatches(filename, mediaType string) bool {
	extension := strings.ToLower(filepath.Ext(filename))
	switch mediaType {
	case "image/png":
		return extension == ".png"
	case "image/jpeg":
		return extension == ".jpg" || extension == ".jpeg"
	case "image/gif":
		return extension == ".gif"
	case "image/webp":
		return extension == ".webp"
	default:
		return false
	}
}

func (c *desktopController) syncDesktopImages(task *workerprotocol.Task,
	images []workerprotocol.DesktopImage,
) {
	ctx, cancel := context.WithTimeout(c.processor.workspaces.ctx, 2*time.Minute)
	defer cancel()
	waitDeadline := time.NewTimer(time.Minute)
	defer waitDeadline.Stop()
	for {
		requestCtx, requestCancel := context.WithTimeout(ctx, c.controlTimeout())
		target, err := c.processor.client.DesktopImageTarget(requestCtx, task.Claimed.ID)
		requestCancel()
		if err == nil && (target.Status == "ready" || target.Status == "complete") {
			if target.Status == "complete" {
				return
			}
			break
		}
		if err != nil && !retryableDesktopControlError(err) {
			c.processor.logger.Warn("读取 Desktop 图片投影目标失败",
				zap.String("intent_id", task.Claimed.ID.String()), zap.Error(err))
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-waitDeadline.C:
			c.processor.logger.Warn("等待 Desktop 图片投影目标超时",
				zap.String("intent_id", task.Claimed.ID.String()))
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
	for ordinal, image := range images {
		if image.Error != "" || image.SourcePath == "" {
			continue
		}
		var uploadErr error
		for attempt := 0; attempt < 3; attempt++ {
			requestCtx, requestCancel := context.WithTimeout(ctx, c.controlTimeout())
			var source io.ReadCloser
			var size int64
			source, size, uploadErr = c.openDesktopImage(requestCtx, image.SourcePath)
			if uploadErr == nil && size != image.Size {
				uploadErr = errors.New("desktop 图片在上传前发生变化")
			}
			if uploadErr == nil {
				_, uploadErr = c.processor.client.UploadDesktopImageReader(requestCtx,
					task.Claimed.ID, ordinal, image, attempt == 2, source)
			}
			if source != nil {
				if closeErr := source.Close(); uploadErr == nil {
					uploadErr = closeErr
				}
			}
			requestCancel()
			if uploadErr == nil {
				break
			}
			if !retryableDesktopControlError(uploadErr) || attempt == 2 {
				break
			}
			if !waitContext(ctx, time.Duration(attempt+1)*500*time.Millisecond) {
				return
			}
		}
		if uploadErr == nil {
			continue
		}
		c.processor.logger.Warn("同步 Desktop 图片到 Discord 失败",
			zap.String("intent_id", task.Claimed.ID.String()),
			zap.String("filename", image.Filename), zap.Error(uploadErr))
		requestCtx, requestCancel := context.WithTimeout(ctx, c.controlTimeout())
		_ = c.processor.client.FailDesktopImage(requestCtx, task.Claimed.ID, ordinal, uploadErr)
		requestCancel()
	}
}

func (c *desktopController) observeDesktopSteer(call appserverhub.Call,
	result json.RawMessage,
) {
	ctx := c.processor.workspaces.ctx
	request := workerprotocol.DesktopSteerRecordRequest{
		WorkspaceID: c.workspace.runtime.WorkspaceID,
		RequestKey:  desktopRequestKey(call.Method, call.Params, result),
		Params:      call.Params,
	}
	for attempt := 0; attempt < 8 && ctx.Err() == nil; attempt++ {
		requestCtx, cancel := context.WithTimeout(ctx, c.processor.cfg.ControlTimeout)
		err := c.processor.client.RecordDesktopSteer(requestCtx, request)
		cancel()
		if err == nil {
			return
		}
		c.processor.logger.Warn("异步记录 Desktop Steer 失败，Desktop Steer 已继续执行",
			zap.String("request_key", request.RequestKey), zap.Error(err))
		if !waitContext(ctx, 500*time.Millisecond) {
			return
		}
	}
}

func desktopRuntimeForTask(workspaceRoot string, workspaceID uuid.UUID,
	task *workerprotocol.Task,
) (hostWorkspaceRuntime, error) {
	if task == nil || task.Snapshot.Session == nil ||
		task.Snapshot.Session.Project == nil {
		return hostWorkspaceRuntime{}, errors.New("desktop turn 缺少 Workspace 快照")
	}
	projectContext := task.Snapshot.Session.Project
	if projectContext.WorkspaceID == uuid.Nil || projectContext.WorkspaceID != workspaceID {
		return hostWorkspaceRuntime{}, errors.New("desktop turn Workspace 快照与 Worker 绑定不一致")
	}
	workspace, err := hostWorkspacePath(workspaceRoot, projectContext.WorkspaceRelative)
	if err != nil {
		return hostWorkspaceRuntime{}, err
	}
	return hostWorkspaceRuntime{Workspace: workspace, ProjectKind: projectContext.WorkspaceKind,
		RemoteURL: projectContext.CloneURL}, nil
}

func (c *desktopController) finishDesktopTurn(ctx context.Context, task *workerprotocol.Task,
	reporter *desktopEventReporter, result codexcontrol.TurnResult, cause error,
) {
	reporter.Finish(result, cause)
}

func (c *desktopController) desktopTurnHeartbeat(ctx context.Context,
	task *workerprotocol.Task, commands chan<- workerprotocol.RunCommand,
) {
	ticker := time.NewTicker(c.processor.cfg.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			requestCtx, cancel := context.WithTimeout(ctx, c.processor.cfg.ControlTimeout)
			response, err := c.processor.client.RunHeartbeat(requestCtx, task)
			cancel()
			if err == nil {
				deliverCommands(commands, response.Commands)
			}
		}
	}
}

func (c *desktopController) cleanupDesktopCall(plan appserverhub.CallPlan, cause error) {
	switch state := plan.State.(type) {
	case *desktopCallState:
		state.toolReady <- desktopToolRuntime{err: cause}
		state.subscription.Close()
		state.unbind()
		state.unbindInput()
	case *desktopThreadCallState:
		go c.failDesktopThread(state.request, cause)
	}
}

func (c *desktopController) answerDesktopInteractive(ctx context.Context,
	input workerprotocol.InteractiveAnswerRequest,
) (workerprotocol.InteractiveState, error) {
	var state workerprotocol.InteractiveState
	var err error
	for attempt := 0; attempt < 8; attempt++ {
		requestCtx, cancel := context.WithTimeout(ctx, c.processor.cfg.ControlTimeout)
		state, err = c.processor.client.AnswerInteractive(requestCtx, input)
		cancel()
		if err == nil {
			return state, nil
		}
		if !waitContext(ctx, 100*time.Millisecond) {
			break
		}
	}
	return workerprotocol.InteractiveState{}, err
}

func (c *desktopController) compensateDesktopInteractive(input workerprotocol.InteractiveAnswerRequest) {
	ctx, cancel := context.WithTimeout(c.processor.workspaces.ctx, time.Minute)
	defer cancel()
	for ctx.Err() == nil {
		requestCtx, requestCancel := context.WithTimeout(ctx, c.processor.cfg.ControlTimeout)
		_, err := c.processor.client.AnswerInteractive(requestCtx, input)
		requestCancel()
		if err == nil {
			return
		}
		if !waitContext(ctx, time.Second) {
			return
		}
	}
}

func desktopRequestKey(method string, values ...json.RawMessage) string {
	digest := sha256.New()
	_, _ = digest.Write([]byte(method))
	for _, value := range values {
		_, _ = digest.Write(value)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func callScope(raw json.RawMessage) (string, string) {
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

type desktopEventReporter struct {
	ctx       context.Context
	processor *Processor
	task      *workerprotocol.Task
	mu        sync.Mutex
	journal   *runJournal
}

func newDesktopEventReporter(ctx context.Context, processor *Processor,
	task *workerprotocol.Task,
) *desktopEventReporter {
	journal := &runJournal{Task: *task, NextSequence: 1}
	reporter := &desktopEventReporter{ctx: ctx, processor: processor, task: task,
		journal: journal}
	reporter.saveLocked()
	return reporter
}

func (r *desktopEventReporter) Report(eventType string, payload json.RawMessage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.journal.PendingEvents = append(r.journal.PendingEvents, workerprotocol.EventInput{
		Sequence: r.journal.NextSequence,
		Type:     eventType, Payload: append(json.RawMessage(nil), payload...)})
	r.journal.NextSequence++
	r.saveLocked()
	r.flushLocked()
}

func (r *desktopEventReporter) Flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flushLocked()
}

func (r *desktopEventReporter) flushLocked() {
	if len(r.journal.PendingEvents) == 0 {
		return
	}
	requestCtx, cancel := context.WithTimeout(r.ctx, r.processor.cfg.ControlTimeout)
	err := r.processor.client.Events(requestCtx, r.task, r.journal.PendingEvents)
	cancel()
	if err == nil {
		r.journal.PendingEvents = nil
		r.saveLocked()
	} else {
		r.processor.logger.Warn("上传 Desktop Turn 事件失败，已保留在 Journal", zap.Error(err))
	}
}

func (r *desktopEventReporter) Finish(result codexcontrol.TurnResult, cause error) {
	r.mu.Lock()
	if cause == nil {
		copyResult := result
		r.journal.Result = &copyResult
	} else {
		r.journal.FailureCode, r.journal.Failure = "desktop_turn_error", cause.Error()
		if errors.Is(cause, errRemoteInterrupt) {
			r.journal.FailureCode = "user_interrupt"
		}
		var codexErr *workerprotocol.CodexTurnError
		if errors.As(cause, &codexErr) && !codexErr.WillRetry {
			r.journal.FailureCode = "codex_non_retryable_error"
			r.journal.CodexError = codexErr
		}
	}
	r.saveLocked()
	r.mu.Unlock()
	for r.ctx.Err() == nil {
		r.Flush()
		requestCtx, cancel := context.WithTimeout(r.ctx, r.processor.cfg.ControlTimeout)
		var err error
		if r.journal.Result != nil {
			err = r.processor.client.Complete(requestCtx, r.task, *r.journal.Result)
		} else {
			err = r.processor.client.FailWithCodexError(requestCtx, r.task,
				r.journal.FailureCode, errors.New(r.journal.Failure), r.journal.CodexError)
		}
		cancel()
		if err == nil || workerprotocol.IsAlreadyFinished(err) {
			if r.processor.journals != nil {
				_ = r.processor.journals.remove(r.task.Claimed.RunID)
			}
			return
		}
		if workerprotocol.IsLeaseLost(err) {
			r.processor.logger.Error("Desktop Run Lease 已失效，停止补交", zap.Error(err))
			return
		}
		r.processor.logger.Warn("提交 Desktop Turn 终态失败，稍后重试", zap.Error(err))
		if !waitContext(r.ctx, 3*time.Second) {
			return
		}
	}
}

func (r *desktopEventReporter) saveLocked() {
	if r.processor.journals == nil {
		return
	}
	if err := r.processor.journals.save(r.journal); err != nil {
		r.processor.logger.Error("持久化 Desktop Run Journal 失败", zap.Error(err))
	}
}

var _ appserverhub.Controller = (*desktopController)(nil)
var _ appserverhub.ArchiveGate = (*desktopController)(nil)
var _ appserverhub.EphemeralThreadConfigurator = (*desktopController)(nil)
