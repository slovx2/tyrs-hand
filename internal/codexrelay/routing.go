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
	controlled := class == methodControlled && source.role == RoleDesktop
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
	if method == "thread/resume" {
		threadID, _ := threadScope(plan.Params)
		if cached := r.cachedThread(threadID); len(cached) > 0 {
			source.subscribe(threadID)
			if controlled {
				return r.completeControlled(ctx, call, plan, cached, nil)
			}
			return cached, nil
		}
	}
	var result json.RawMessage
	if err := r.upstream.Call(ctx, method, plan.Params, &result); err != nil {
		if controlled {
			return r.completeControlled(ctx, call, plan, nil, err)
		}
		return nil, err
	}
	if method == "thread/list" {
		result = r.mergeThreadList(result)
	}
	if method == "thread/start" || method == "thread/fork" || method == "thread/resume" {
		threadID := responseThreadID(result)
		if threadID == "" {
			return nil, fmt.Errorf("%s 没有返回 Codex Thread ID", method)
		}
		r.subscribeCreatedThread(source, threadID)
		r.cacheThread(threadID, result)
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

func (r *Relay) subscribeCreatedThread(source *session, threadID string) {
	r.mu.Lock()
	sessions := make([]*session, 0, len(r.sessions))
	for _, item := range r.sessions {
		if item == source || item.role == RoleWorker {
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
	r.forgetThread(threadID)
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

func (r *Relay) cachedThread(threadID string) json.RawMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append(json.RawMessage(nil), r.threads[threadID]...)
}

func (r *Relay) cacheThread(threadID string, response json.RawMessage) {
	if threadID == "" || len(response) == 0 {
		return
	}
	r.mu.Lock()
	r.threads[threadID] = append(json.RawMessage(nil), response...)
	r.mu.Unlock()
}

func (r *Relay) forgetThread(threadID string) {
	r.mu.Lock()
	delete(r.threads, threadID)
	r.mu.Unlock()
}

func (r *Relay) mergeThreadList(response json.RawMessage) json.RawMessage {
	var value struct {
		Data       []json.RawMessage `json:"data"`
		NextCursor json.RawMessage   `json:"nextCursor"`
	}
	if json.Unmarshal(response, &value) != nil {
		return response
	}
	known := make(map[string]bool, len(value.Data))
	for _, thread := range value.Data {
		known[threadObjectID(thread)] = true
	}
	r.mu.Lock()
	cached := make([]json.RawMessage, 0, len(r.threads))
	for _, item := range r.threads {
		cached = append(cached, append(json.RawMessage(nil), item...))
	}
	r.mu.Unlock()
	for _, item := range cached {
		var started struct {
			Thread json.RawMessage `json:"thread"`
		}
		if json.Unmarshal(item, &started) != nil || len(started.Thread) == 0 {
			continue
		}
		if id := threadObjectID(started.Thread); id != "" && !known[id] {
			value.Data = append(value.Data, started.Thread)
			known[id] = true
		}
	}
	merged, err := json.Marshal(value)
	if err != nil {
		return response
	}
	return merged
}

func threadObjectID(raw json.RawMessage) string {
	var value struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &value)
	return value.ID
}

func (r *Relay) forwardEvents() {
	for event := range r.upstreamEvents.Events() {
		threadID, _ := threadScope(event.Params)
		r.mu.Lock()
		sessions := make([]*session, 0, len(r.sessions))
		for _, item := range r.sessions {
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
