package codexrelay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
)

func (r *Relay) routeCall(ctx context.Context, source *session, method string,
	params json.RawMessage,
) (json.RawMessage, error) {
	class := classifyMethod(method)
	if class == methodLocal {
		return r.upstream.InitializeResult(), nil
	}
	// 未知方法也透明交给固定版本的 app-server 判定，避免 Relay 升级滞后破坏 Desktop 新能力。
	call := Call{Role: source.role, Method: method, Params: append(json.RawMessage(nil), params...)}
	plan := CallPlan{Params: params, Forward: true}
	ephemeral := r.callIsEphemeral(method, params)
	controlled := class == methodControlled && source.role == RoleDesktop && !ephemeral
	if controlled {
		if r.options.Controller == nil {
			return nil, &ProtocolError{Code: -32041,
				Message: "Codex Relay 尚未配置 Desktop Control"}
		}
		var err error
		plan, err = r.options.Controller.PrepareCall(ctx, call)
		if err != nil {
			return nil, err
		}
		if len(plan.Params) == 0 {
			plan.Params = params
		}
		if !plan.Forward {
			return r.completeControlled(ctx, call, plan, plan.Result, nil)
		}
	}
	if method == "thread/unsubscribe" {
		result, err := r.unsubscribe(ctx, source, plan.Params)
		if controlled {
			return r.completeControlled(ctx, call, plan, result, err)
		}
		return result, err
	}
	var result json.RawMessage
	if err := r.upstream.Call(ctx, method, plan.Params, &result); err != nil {
		if controlled {
			return r.completeControlled(ctx, call, plan, nil, err)
		}
		return nil, err
	}
	if method == "thread/start" || method == "thread/fork" || method == "thread/resume" {
		threadID := responseThreadID(result)
		if threadID == "" {
			return nil, fmt.Errorf("%s 没有返回 Codex Thread ID", method)
		}
		if responseThreadEphemeral(result) || ephemeral {
			r.markEphemeral(threadID)
			ephemeral = true
		}
		r.subscribeCreatedThread(source, threadID, ephemeral)
	}
	if controlled {
		return r.completeControlled(ctx, call, plan, result, nil)
	}
	return result, nil
}

func (r *Relay) completeControlled(ctx context.Context, call Call, plan CallPlan,
	result json.RawMessage, cause error,
) (json.RawMessage, error) {
	completed, err := r.options.Controller.CompleteCall(ctx, call, plan, result, cause)
	if err != nil {
		return nil, err
	}
	return completed, nil
}

func (r *Relay) routeNotification(source *session, method string, params json.RawMessage) error {
	if method == "initialized" {
		return nil
	}
	return r.upstream.Notify(method, params)
}

func (r *Relay) subscribeCreatedThread(source *session, threadID string, ephemeral bool) {
	r.mu.Lock()
	sessions := make([]*session, 0, len(r.sessions))
	for _, item := range r.sessions {
		if item == source || !ephemeral {
			sessions = append(sessions, item)
		}
	}
	r.mu.Unlock()
	for _, item := range sessions {
		item.subscribe(threadID)
	}
}

func (r *Relay) unsubscribe(ctx context.Context, source *session,
	params json.RawMessage,
) (json.RawMessage, error) {
	threadID, _ := threadScope(params)
	if threadID == "" {
		return nil, errors.New("thread/unsubscribe 缺少 threadId")
	}
	wasSubscribed := source.unsubscribe(threadID)
	if r.anySubscribed(threadID) {
		status := "notSubscribed"
		if wasSubscribed {
			status = "unsubscribed"
		}
		return json.Marshal(map[string]string{"status": status})
	}
	var result json.RawMessage
	if err := r.upstream.Call(ctx, "thread/unsubscribe", params, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (r *Relay) anySubscribed(threadID string) bool {
	r.mu.Lock()
	sessions := make([]*session, 0, len(r.sessions))
	for _, item := range r.sessions {
		sessions = append(sessions, item)
	}
	r.mu.Unlock()
	for _, item := range sessions {
		if item.subscribed(threadID) {
			return true
		}
	}
	return false
}

func (r *Relay) forwardEvents() {
	for event := range r.upstreamEvents.Events() {
		threadID, _ := threadScope(event.Params)
		if event.Method == "thread/started" && eventThreadEphemeral(event.Params) {
			r.markEphemeral(threadID)
		}
		ephemeral := r.isEphemeral(threadID)
		r.mu.Lock()
		sessions := make([]*session, 0, len(r.sessions))
		for _, item := range r.sessions {
			if ephemeral && item.role == RoleWorker {
				continue
			}
			if threadID == "" || event.Method == "thread/started" || item.subscribed(threadID) {
				sessions = append(sessions, item)
			}
		}
		r.mu.Unlock()
		for _, item := range sessions {
			if err := item.publish(event); err != nil {
				r.removeSession(item)
			}
		}
	}
	select {
	case <-r.done:
	default:
		r.shutdown(errors.New("relay 上游事件流已关闭"))
	}
}

func (r *Relay) callIsEphemeral(method string, params json.RawMessage) bool {
	if method == "thread/start" || method == "thread/fork" {
		var value struct {
			Ephemeral bool   `json:"ephemeral"`
			ThreadID  string `json:"threadId"`
		}
		_ = json.Unmarshal(params, &value)
		return value.Ephemeral || r.isEphemeral(value.ThreadID)
	}
	threadID, _ := threadScope(params)
	return r.isEphemeral(threadID)
}

func (r *Relay) markEphemeral(threadID string) {
	if threadID == "" {
		return
	}
	r.mu.Lock()
	r.ephemeralThreads[threadID] = true
	r.mu.Unlock()
}

func (r *Relay) isEphemeral(threadID string) bool {
	if threadID == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ephemeralThreads[threadID]
}

func responseThreadEphemeral(raw json.RawMessage) bool {
	var value struct {
		Thread struct {
			Ephemeral bool `json:"ephemeral"`
		} `json:"thread"`
	}
	_ = json.Unmarshal(raw, &value)
	return value.Thread.Ephemeral
}

func eventThreadEphemeral(raw json.RawMessage) bool {
	return responseThreadEphemeral(raw)
}

func (r *Relay) handleServerRequest(ctx context.Context,
	request codex.ServerRequest,
) (any, error) {
	threadID, _ := threadScope(request.Params)
	switch request.Method {
	case "item/tool/call":
		worker := r.workerForThread(threadID)
		if worker == nil {
			return nil, errors.New("当前 Thread 没有活动的 Worker 工具执行器")
		}
		return worker.invoke(ctx, request)
	case "item/tool/requestUserInput":
		return r.firstInteractiveAnswer(ctx, request, threadID)
	default:
		// 普通 Codex 能力（例如命令审批）优先保持 Desktop 原生行为；共享配置仍由客户端方法分类控制。
		return r.firstDesktopAnswer(ctx, request, threadID)
	}
}

func (r *Relay) firstDesktopAnswer(ctx context.Context, request codex.ServerRequest,
	threadID string,
) (any, error) {
	targets := r.interactiveTargets(threadID)
	filtered := targets[:0]
	for _, target := range targets {
		if target.role == RoleDesktop {
			filtered = append(filtered, target)
		}
	}
	if len(filtered) == 0 {
		return nil, fmt.Errorf("server request %q 没有可用的 Desktop 客户端", request.Method)
	}
	answerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	outcomes := make(chan serverOutcome, len(filtered))
	for _, target := range filtered {
		target := target
		go func() {
			result, err := target.invoke(answerCtx, request)
			outcomes <- serverOutcome{result: result, err: err, role: target.role}
		}()
	}
	var lastErr error
	for range filtered {
		select {
		case outcome := <-outcomes:
			if outcome.err == nil {
				cancel()
				return outcome.result, nil
			}
			lastErr = outcome.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, lastErr
}

func (r *Relay) workerForThread(threadID string) *session {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, item := range r.sessions {
		if item.role == RoleWorker && (threadID == "" || item.subscribed(threadID)) {
			return item
		}
	}
	return nil
}

func (r *Relay) firstInteractiveAnswer(ctx context.Context, request codex.ServerRequest,
	threadID string,
) (any, error) {
	targets := r.interactiveTargets(threadID)
	if len(targets) == 0 {
		return nil, errors.New("requestUserInput 没有可用的 Desktop 或 Worker 客户端")
	}
	answerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	outcomes := make(chan serverOutcome, len(targets))
	for _, target := range targets {
		target := target
		copyRequest := request
		if target.role == RoleDesktop {
			copyRequest.Params = withoutAutoResolution(request.Params)
		}
		go func() {
			result, err := target.invoke(answerCtx, copyRequest)
			outcomes <- serverOutcome{result: result, err: err, role: target.role}
		}()
	}
	var lastErr error
	for range targets {
		select {
		case outcome := <-outcomes:
			if outcome.err == nil {
				if r.options.Controller == nil {
					cancel()
					return outcome.result, nil
				}
				won, resolved, err := r.options.Controller.ResolveInteractive(
					ctx, request, outcome.result, outcome.role)
				if err != nil {
					lastErr = err
					continue
				}
				if won {
					cancel()
					return resolved, nil
				}
			}
			if outcome.err != nil {
				lastErr = outcome.err
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if lastErr == nil {
		lastErr = errors.New("所有 requestUserInput 客户端均未返回答案")
	}
	return nil, lastErr
}

func (r *Relay) interactiveTargets(threadID string) []*session {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]*session, 0, len(r.sessions))
	for _, item := range r.sessions {
		if threadID == "" || item.subscribed(threadID) {
			result = append(result, item)
		}
	}
	return result
}

func withoutAutoResolution(params json.RawMessage) json.RawMessage {
	var value map[string]any
	if json.Unmarshal(params, &value) != nil {
		return params
	}
	delete(value, "autoResolutionMs")
	result, _ := json.Marshal(value)
	return result
}

func responseThreadID(result json.RawMessage) string {
	var value struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	_ = json.Unmarshal(result, &value)
	return value.Thread.ID
}

func requestContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, timeout)
}
