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
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/codexrelay"
	"github.com/slovx2/tyrs-hand/internal/devcontainer"
	"github.com/slovx2/tyrs-hand/internal/participantidentity"
	"github.com/slovx2/tyrs-hand/internal/ports"
	"github.com/slovx2/tyrs-hand/internal/settings"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"go.uber.org/zap"
)

type desktopRelayController struct {
	processor   *RemoteProcessor
	environment *environmentCodex
}

type desktopRelayCallState struct {
	subscription *codexrelay.Subscription
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
	runtime devcontainer.Runtime
	report  func(string, json.RawMessage)
	err     error
}

func (c *desktopRelayController) PrepareCall(ctx context.Context,
	call codexrelay.Call,
) (codexrelay.CallPlan, error) {
	plan := codexrelay.CallPlan{Params: append(json.RawMessage(nil), call.Params...), Forward: true}
	if call.Method == "turn/start" || call.Method == "turn/steer" {
		if identity, ok := c.environment.sshParticipant(); ok {
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
	case "model/list":
		if call.Role == codexrelay.RoleDesktop &&
			c.environment.runtime.ModelSource == settings.ModelSourceProvider {
			result, err := providerDesktopModelCatalog()
			if err != nil {
				return plan, err
			}
			plan.Forward = false
			plan.Result = result
		}
	case "thread/list":
		if call.Role == codexrelay.RoleDesktop {
			plan.Params = desktopThreadListAllProviders(call.Params)
		}
	case "thread/start":
		if call.Role == codexrelay.RoleDesktop {
			state, err := c.prepareDesktopThread(ctx, call)
			if err != nil {
				return plan, err
			}
			plan.State = &desktopThreadCallState{request: state}
		}
	case "thread/fork":
		if call.Role == codexrelay.RoleDesktop {
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
					EnvironmentID: c.environment.runtime.EnvironmentID, Params: plan.Params,
				})
			cancel()
			if err != nil {
				return plan, err
			}
			if len(preflight.Params) > 0 {
				plan.Params = preflight.Params
			}
		}
		threadID, _ := relayCallScope(plan.Params)
		if threadID == "" {
			return plan, nil
		}
		state := &desktopRelayCallState{
			subscription: c.environment.client.Subscribe(codex.ThreadFilter{ThreadID: threadID}),
			toolReady:    make(chan desktopToolRuntime, 1),
		}
		state.unbind = c.environment.bindTool(threadID, func(ctx context.Context,
			request codex.ToolCallRequest,
		) (codex.ToolCallResult, error) {
			select {
			case runtime := <-state.toolReady:
				state.toolReady <- runtime
				if runtime.err != nil {
					return codex.ToolCallResult{}, runtime.err
				}
				return c.processor.handleRemoteDiscordTool(ctx, runtime.task,
					runtime.runtime, request, runtime.report)
			case <-ctx.Done():
				return codex.ToolCallResult{}, ctx.Err()
			case <-time.After(10 * time.Second):
				return codex.ToolCallResult{}, errors.New("动态工具尚未完成 Discord Control 绑定")
			}
		})
		state.unbindInput = c.environment.bindInteractive(threadID,
			func(ctx context.Context, request codex.ServerRequest) (any, error) {
				select {
				case runtime := <-state.toolReady:
					state.toolReady <- runtime
					if runtime.err != nil {
						return nil, runtime.err
					}
					return c.processor.handleRemoteInteractive(ctx, runtime.task,
						c.environment.generation, request)
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(10 * time.Second):
					return nil, errors.New("desktop 交互尚未完成 Discord Control 绑定")
				}
			})
		plan.State = state
	case "thread/rollback":
		requestCtx, cancel := context.WithTimeout(ctx, c.processor.cfg.ControlTimeout)
		state, err := c.processor.client.PrepareDesktopRollback(requestCtx,
			workerprotocol.DesktopRollbackPrepareRequest{
				EnvironmentID: c.environment.runtime.EnvironmentID,
				RequestKey:    desktopRequestKey(call.Method, plan.Params, nil), Params: plan.Params,
			})
		cancel()
		if err != nil {
			return plan, err
		}
		plan.State = &desktopRollbackCallState{request: state}
	case "thread/archive", "thread/unarchive":
		threadID, _ := relayCallScope(plan.Params)
		if threadID == "" {
			return plan, nil
		}
		desiredState := "archived"
		if call.Method == "thread/unarchive" {
			desiredState = "active"
		}
		requestCtx, cancel := context.WithTimeout(c.processor.environments.ctx,
			c.processor.cfg.ControlTimeout)
		state, err := c.processor.client.PrepareDesktopThreadLifecycle(requestCtx,
			workerprotocol.ThreadLifecyclePrepareRequest{
				EnvironmentID: c.environment.runtime.EnvironmentID,
				ThreadID:      threadID, DesiredState: desiredState,
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

func (c *desktopRelayController) CompleteCall(_ context.Context, call codexrelay.Call,
	plan codexrelay.CallPlan, result json.RawMessage, cause error,
) (json.RawMessage, error) {
	if call.Method == "account/read" {
		return desktopAccountForModelSource(c.environment.runtime.ModelSource, result, cause)
	}
	if lifecycle, ok := plan.State.(*desktopLifecycleCallState); ok {
		go c.completeDesktopLifecycle(lifecycle.request, result, cause)
	}
	if rollback, ok := plan.State.(*desktopRollbackCallState); ok {
		var requestErr *codex.RequestError
		if cause != nil && errors.As(cause, &requestErr) && requestErr.State == codex.RequestUnknown {
			runtime := codex.NewRuntime(c.environment.client)
			if snapshot, err := runtime.ReadThread(c.processor.environments.ctx,
				rollback.request.ThreadID); err == nil {
				if _, exists := snapshot.TurnByID(rollback.request.TargetTurnID); !exists {
					cause = nil
					result = json.RawMessage(`{}`)
				}
			}
		}
		request := workerprotocol.DesktopRollbackCompleteRequest{
			EnvironmentID: rollback.request.EnvironmentID, Response: result,
		}
		if cause != nil {
			request.Error = cause.Error()
		}
		ctx, cancel := context.WithTimeout(c.processor.environments.ctx,
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
				ctx, cancel := context.WithTimeout(c.processor.environments.ctx,
					c.processor.cfg.ControlTimeout)
				_ = c.processor.client.CompleteDesktopRollback(ctx, reservationID,
					workerprotocol.DesktopRollbackCompleteRequest{
						EnvironmentID: c.environment.runtime.EnvironmentID, Error: cause.Error(),
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
			go c.environment.recordThreadName(c.processor.environments.ctx, threadID, name)
		}
		if threadID, model, effort, tier := desktopThreadRuntime(result); threadID != "" {
			go c.environment.recordThreadSettings(c.processor.environments.ctx, threadID,
				model, effort, tier, "", "desktop")
		}
	case "thread/resume":
		if threadID, name := desktopThreadName(result); threadID != "" && name != "" {
			go c.environment.recordThreadName(c.processor.environments.ctx, threadID, name)
		}
		if threadID, model, effort, tier := desktopThreadRuntime(result); threadID != "" {
			go c.environment.recordThreadSettings(c.processor.environments.ctx, threadID,
				model, effort, tier, "", "desktop")
		}
	case "turn/start":
		state, _ := plan.State.(*desktopRelayCallState)
		if state != nil {
			call.Params = plan.Params
			go c.observeDesktopTurn(call, result, state)
		}
	case "turn/steer":
		go c.observeDesktopSteer(call, result)
	}
	return result, nil
}

func (c *desktopRelayController) completeDesktopLifecycle(
	state workerprotocol.ThreadLifecycleState, result json.RawMessage, cause error,
) {
	if state.ID == uuid.Nil {
		return
	}
	request := workerprotocol.ThreadLifecycleCompleteRequest{
		EnvironmentID: state.EnvironmentID, Response: result,
	}
	if cause != nil {
		request.Error = cause.Error()
	}
	ctx := c.processor.environments.ctx
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

func (c *desktopRelayController) WaitArchiveReady(ctx context.Context, _ codexrelay.Call,
	plan codexrelay.CallPlan,
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

func desktopAccountForModelSource(modelSource string, result json.RawMessage,
	cause error,
) (json.RawMessage, error) {
	if modelSource != settings.ModelSourceProvider {
		return result, cause
	}
	// Provider 的模型能力由 App Key 决定，不能再被仅用于插件市场的 ChatGPT 套餐过滤。
	return json.RawMessage(`{"account":{"type":"chatgpt","email":null,` +
		`"planType":"unknown"},"requiresOpenaiAuth":false}`), nil
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

func (c *desktopRelayController) ConfigureEphemeralThread(_ context.Context,
	call codexrelay.Call,
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

func (c *desktopRelayController) ResolveInteractive(ctx context.Context,
	request codex.ServerRequest, answer json.RawMessage, surface codexrelay.Role,
) (bool, json.RawMessage, error) {
	if surface != codexrelay.RoleDesktop {
		return true, answer, nil
	}
	threadID, turnID, itemID := serverRequestScope(request.Params)
	input := workerprotocol.InteractiveAnswerRequest{
		EnvironmentID: c.environment.runtime.EnvironmentID, ThreadID: threadID,
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

func (c *desktopRelayController) configureDesktopThreadRuntime(call codexrelay.Call,
	params json.RawMessage,
) json.RawMessage {
	if call.Role != codexrelay.RoleDesktop {
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

func (c *desktopRelayController) injectDesktopRuntime(params json.RawMessage,
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
	applyModelProviderConfig(config, c.environment.runtime.ModelSource,
		c.environment.runtime.ModelBaseURL)
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

func (c *desktopRelayController) desktopWorkspaceAllowsPublish(cwd string) bool {
	c.environment.mu.Lock()
	forums := append([]workerprotocol.EnvironmentForum(nil), c.environment.manifest.Forums...)
	c.environment.mu.Unlock()
	for _, forum := range forums {
		workspace, err := devcontainer.ContainerWorkspacePath(forum.WorkspaceRelative)
		if err == nil && workspace == cwd {
			return forum.WorkspaceKind == "git"
		}
	}
	return false
}

func (c *desktopRelayController) prepareDesktopThread(ctx context.Context,
	call codexrelay.Call,
) (workerprotocol.DesktopThreadState, error) {
	requestCtx, cancel := context.WithTimeout(ctx, c.controlTimeout())
	defer cancel()
	return c.processor.client.PrepareDesktopThread(requestCtx,
		workerprotocol.DesktopThreadPrepareRequest{
			EnvironmentID: c.environment.runtime.EnvironmentID,
			Operation:     strings.TrimPrefix(call.Method, "thread/"),
			RequestKey: desktopRequestKey(call.Method, call.Params,
				json.RawMessage(uuid.NewString())),
			Params: call.Params,
		})
}

func (c *desktopRelayController) completeDesktopThread(
	state workerprotocol.DesktopThreadState, result json.RawMessage,
) {
	if state.ID == uuid.Nil {
		return
	}
	ctx := c.processor.environments.ctx
	for attempt := 0; attempt < 8 && ctx.Err() == nil; attempt++ {
		requestCtx, cancel := context.WithTimeout(ctx, c.controlTimeout())
		_, err := c.processor.client.CompleteDesktopThread(requestCtx, state.ID,
			workerprotocol.DesktopThreadCompleteRequest{
				EnvironmentID: c.environment.runtime.EnvironmentID, Response: result,
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

func (c *desktopRelayController) failDesktopThread(
	state workerprotocol.DesktopThreadState, cause error,
) {
	if state.ID == uuid.Nil || cause == nil {
		return
	}
	ctx := c.processor.environments.ctx
	for attempt := 0; attempt < 8 && ctx.Err() == nil; attempt++ {
		requestCtx, cancel := context.WithTimeout(ctx, c.controlTimeout())
		err := c.processor.client.FailDesktopThread(requestCtx, state.ID,
			workerprotocol.DesktopThreadFailRequest{
				EnvironmentID: c.environment.runtime.EnvironmentID, Error: cause.Error(),
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

func (c *desktopRelayController) controlTimeout() time.Duration {
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

func (c *desktopRelayController) observeDesktopTurn(call codexrelay.Call,
	result json.RawMessage, state *desktopRelayCallState,
) {
	defer state.subscription.Close()
	defer state.unbind()
	defer state.unbindInput()
	threadID, _ := relayCallScope(call.Params)
	_, turnID := relayCallScope(result)
	if threadID == "" || turnID == "" {
		state.toolReady <- desktopToolRuntime{err: errors.New("turn/start 响应缺少 Codex Turn ID")}
		return
	}
	ctx := c.processor.environments.ctx
	requestKey := desktopRequestKey(call.Method, call.Params, result)
	images, imageNotice, imageErr := desktopImagesFromTurn(call.Params)
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
				EnvironmentID: c.environment.runtime.EnvironmentID,
				WorkerID:      c.processor.cfg.WorkerID, RequestKey: requestKey,
				Params: call.Params, Images: images, ImageError: imageNotice,
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
	toolRuntime, runtimeErr := desktopRuntimeForTask(c.environment.runtime, &task)
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
	runtime := codex.NewRuntime(c.environment.client)
	resultValue, err := c.processor.waitRemoteTurn(ctx, runtime, state.subscription.Events(),
		&task, threadID, turnID, commands,
		c.processor.discordCommandHandler(&task, c.environment.runtime, []ports.SkillRef{}, reporter.Report),
		remoteDiscordEventReporter(reporter.Report))
	cancelHeartbeat()
	if err == nil {
		reporter.Report("discord.progress", remoteEventPayload(map[string]string{
			"state": "completed", "detail": "本轮处理完成。",
		}))
	}
	c.finishDesktopTurn(ctx, &task, reporter, resultValue, err)
}

func desktopImagesFromTurn(params json.RawMessage) ([]workerprotocol.DesktopImage, string, error) {
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
		info, err := os.Lstat(path)
		if err != nil || !filepath.IsAbs(path) || !info.Mode().IsRegular() || info.Size() <= 0 ||
			info.Size() > workerprotocol.DesktopImageFileLimit ||
			total+info.Size() > workerprotocol.DesktopImageTotalLimit {
			image.Error = "文件不存在、不是普通文件或超过大小限制"
			images = append(images, image)
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			image.Error = "无法打开文件"
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
			int64(n)+copied != info.Size() {
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
		image.MediaType, image.Size = mediaType, info.Size()
		image.SHA256, image.SourcePath = fmt.Sprintf("%x", digest.Sum(nil)), path
		images = append(images, image)
		total += info.Size()
	}
	notice := ""
	if skipped > 0 {
		notice = fmt.Sprintf("另有 %d 张图片超过 %d 张限制", skipped,
			workerprotocol.DesktopImageCountLimit)
	}
	return images, notice, nil
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

func (c *desktopRelayController) syncDesktopImages(task *workerprotocol.Task,
	images []workerprotocol.DesktopImage,
) {
	ctx, cancel := context.WithTimeout(c.processor.environments.ctx, 2*time.Minute)
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
			_, uploadErr = c.processor.client.UploadDesktopImage(requestCtx,
				task.Claimed.ID, ordinal, image, attempt == 2)
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

func (c *desktopRelayController) observeDesktopSteer(call codexrelay.Call,
	result json.RawMessage,
) {
	ctx := c.processor.environments.ctx
	request := workerprotocol.DesktopSteerRecordRequest{
		EnvironmentID: c.environment.runtime.EnvironmentID,
		RequestKey:    desktopRequestKey(call.Method, call.Params, result),
		Params:        call.Params,
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

func desktopRuntimeForTask(environment devcontainer.Runtime,
	task *workerprotocol.Task,
) (devcontainer.Runtime, error) {
	if task == nil || task.Snapshot.Discord == nil || task.Snapshot.Discord.Development == nil {
		return devcontainer.Runtime{}, errors.New("desktop turn 缺少 Discord 开发环境快照")
	}
	development := task.Snapshot.Discord.Development
	if development.EnvironmentID == uuid.Nil || development.EnvironmentID != environment.EnvironmentID {
		return devcontainer.Runtime{}, errors.New("desktop turn 开发环境快照与 Relay 环境不一致")
	}
	workspace, err := devcontainer.ContainerWorkspacePath(development.WorkspaceRelative)
	if err != nil {
		return devcontainer.Runtime{}, err
	}
	environment.ForumID = development.ForumID
	environment.Workspace = workspace
	return environment, nil
}

func (c *desktopRelayController) finishDesktopTurn(ctx context.Context, task *workerprotocol.Task,
	reporter *desktopEventReporter, result codexcontrol.TurnResult, cause error,
) {
	reporter.Finish(result, cause)
}

func (c *desktopRelayController) desktopTurnHeartbeat(ctx context.Context,
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

func (c *desktopRelayController) cleanupDesktopCall(plan codexrelay.CallPlan, cause error) {
	switch state := plan.State.(type) {
	case *desktopRelayCallState:
		state.toolReady <- desktopToolRuntime{err: cause}
		state.subscription.Close()
		state.unbind()
		state.unbindInput()
	case *desktopThreadCallState:
		go c.failDesktopThread(state.request, cause)
	}
}

func (c *desktopRelayController) answerDesktopInteractive(ctx context.Context,
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

func (c *desktopRelayController) compensateDesktopInteractive(input workerprotocol.InteractiveAnswerRequest) {
	ctx, cancel := context.WithTimeout(c.processor.environments.ctx, time.Minute)
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

func relayCallScope(raw json.RawMessage) (string, string) {
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
	processor *RemoteProcessor
	task      *workerprotocol.Task
	mu        sync.Mutex
	journal   *runJournal
}

func newDesktopEventReporter(ctx context.Context, processor *RemoteProcessor,
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
			err = r.processor.client.Fail(requestCtx, r.task, r.journal.FailureCode,
				errors.New(r.journal.Failure))
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

var _ codexrelay.Controller = (*desktopRelayController)(nil)
var _ codexrelay.ArchiveGate = (*desktopRelayController)(nil)
var _ codexrelay.EphemeralThreadConfigurator = (*desktopRelayController)(nil)
