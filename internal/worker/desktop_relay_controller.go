package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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

type desktopToolRuntime struct {
	task    *workerprotocol.Task
	runtime devcontainer.Runtime
	report  func(string, json.RawMessage)
	err     error
}

func (c *desktopRelayController) PrepareCall(_ context.Context,
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
	switch call.Method {
	case "thread/start":
		plan.Params = c.injectDesktopTools(call.Params)
		plan.Params = participantidentity.AppendDeveloperInstructions(plan.Params)
	case "turn/start":
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
	}
	return plan, nil
}

func (c *desktopRelayController) CompleteCall(_ context.Context, call codexrelay.Call,
	plan codexrelay.CallPlan, result json.RawMessage, cause error,
) (json.RawMessage, error) {
	if call.Method == "account/read" {
		return desktopAccountWithServiceTiers(result, cause)
	}
	if cause != nil {
		c.cleanupDesktopCall(plan, cause)
		return result, cause
	}
	switch call.Method {
	case "thread/start", "thread/fork":
		go c.observeDesktopThread(call, result)
		if threadID, name := desktopThreadName(result); threadID != "" && name != "" {
			go c.environment.recordThreadName(c.processor.environments.ctx, threadID, name)
		}
	case "thread/resume":
		if threadID, name := desktopThreadName(result); threadID != "" && name != "" {
			go c.environment.recordThreadName(c.processor.environments.ctx, threadID, name)
		}
	case "turn/start":
		state, _ := plan.State.(*desktopRelayCallState)
		if state != nil {
			go c.observeDesktopTurn(call, result, state)
		}
	case "turn/steer":
		go c.observeDesktopSteer(call, result)
	}
	return result, nil
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

func desktopAccountWithServiceTiers(result json.RawMessage, cause error) (json.RawMessage, error) {
	var response struct {
		Account *struct {
			Type string `json:"type"`
		} `json:"account"`
	}
	if cause == nil && json.Unmarshal(result, &response) == nil &&
		response.Account != nil && response.Account.Type == "chatgpt" {
		return result, nil
	}
	// Desktop 目前只对 ChatGPT 账号显示 app-server 已声明的速度档位。
	// 环境仍使用原 API Key 调用上游；这里仅补足 Desktop 的能力识别，不暴露虚构账号资料。
	return json.RawMessage(`{"account":{"type":"chatgpt","email":null,` +
		`"planType":"unknown"},"requiresOpenaiAuth":false}`), nil
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

func (c *desktopRelayController) injectDesktopTools(params json.RawMessage) json.RawMessage {
	var value map[string]any
	if json.Unmarshal(params, &value) != nil || c.processor.catalog == nil {
		return params
	}
	githubSpec, err := c.processor.catalog.DynamicToolSpec()
	if err != nil {
		c.processor.logger.Warn("生成 Desktop 动态工具清单失败", zap.Error(err))
		return params
	}
	specs := withBrowserTools(c.processor.cfg, githubSpec, localGitSpec())
	current, _ := value["dynamicTools"].([]any)
	for _, spec := range specs {
		encoded, _ := json.Marshal(spec)
		var item any
		_ = json.Unmarshal(encoded, &item)
		current = append(current, item)
	}
	value["dynamicTools"] = current
	result, err := json.Marshal(value)
	if err != nil {
		return params
	}
	return result
}

func (c *desktopRelayController) observeDesktopThread(call codexrelay.Call,
	result json.RawMessage,
) {
	threadID, _ := relayCallScope(result)
	if threadID == "" {
		return
	}
	requestKey := desktopRequestKey(call.Method, call.Params, result)
	ctx := c.processor.environments.ctx
	var state workerprotocol.DesktopThreadState
	for ctx.Err() == nil {
		requestCtx, cancel := context.WithTimeout(ctx, c.processor.cfg.ControlTimeout)
		var err error
		state, err = c.processor.client.PrepareDesktopThread(requestCtx,
			workerprotocol.DesktopThreadPrepareRequest{
				EnvironmentID: c.environment.runtime.EnvironmentID,
				Operation:     strings.TrimPrefix(call.Method, "thread/"), RequestKey: requestKey,
				Params: call.Params,
			})
		cancel()
		if err == nil {
			break
		}
		c.processor.logger.Warn("异步登记 Desktop Thread 失败，稍后重试",
			zap.String("thread_id", threadID), zap.Error(err))
		if !waitContext(ctx, 3*time.Second) {
			return
		}
	}
	for ctx.Err() == nil {
		if state.Status != "preparing" {
			return
		}
		requestCtx, cancel := context.WithTimeout(ctx, c.processor.cfg.ControlTimeout)
		_, err := c.processor.client.CompleteDesktopThread(requestCtx, state.ID,
			workerprotocol.DesktopThreadCompleteRequest{
				EnvironmentID: c.environment.runtime.EnvironmentID, Response: result,
			})
		cancel()
		if err == nil {
			return
		}
		c.processor.logger.Warn("异步绑定 Desktop Thread 失败，稍后重试",
			zap.String("thread_id", threadID), zap.Error(err))
		if !waitContext(ctx, 500*time.Millisecond) {
			return
		}
	}
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
	var task workerprotocol.Task
	for ctx.Err() == nil {
		requestCtx, cancel := context.WithTimeout(ctx, c.processor.cfg.ControlTimeout)
		var err error
		task, err = c.processor.client.PrepareDesktopTurn(requestCtx,
			workerprotocol.DesktopTurnPrepareRequest{
				EnvironmentID: c.environment.runtime.EnvironmentID,
				WorkerID:      c.processor.cfg.WorkerID, RequestKey: requestKey, Params: call.Params,
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
	state, _ := plan.State.(*desktopRelayCallState)
	if state == nil {
		return
	}
	state.toolReady <- desktopToolRuntime{err: cause}
	state.subscription.Close()
	state.unbind()
	state.unbindInput()
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
