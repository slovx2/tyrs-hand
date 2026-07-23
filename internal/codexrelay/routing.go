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
	var reservedArchive *archiveOperation
	var reservedArchiveLeader bool
	var reservedArchiveThreadID string
	if method == "thread/archive" && source.role == RoleDesktop && !ephemeral {
		reservedArchiveThreadID, _ = threadScope(params)
		if reservedArchiveThreadID != "" {
			reservedArchive, reservedArchiveLeader = r.beginArchive(reservedArchiveThreadID)
		}
	}
	if !ephemeral && (method == "turn/start" || method == "turn/steer") {
		if threadID, _ := threadScope(params); r.archivePending(threadID) {
			return nil, &ProtocolError{Code: -32052,
				Message: "该 Codex Thread 正在归档，不能继续发送新输入"}
		}
	}
	if controlled {
		if r.options.Controller == nil {
			if reservedArchiveLeader {
				r.finishArchive(reservedArchiveThreadID, reservedArchive, nil,
					errors.New("codex Relay 尚未配置 Desktop Control"))
			}
			return nil, &ProtocolError{Code: -32041,
				Message: "Codex Relay 尚未配置 Desktop Control"}
		}
		var err error
		plan, err = r.options.Controller.PrepareCall(ctx, call)
		if err != nil {
			if reservedArchiveLeader {
				r.finishArchive(reservedArchiveThreadID, reservedArchive, nil, err)
			}
			return nil, err
		}
		if len(plan.Params) == 0 {
			plan.Params = params
		}
		if !plan.Forward {
			if reservedArchiveLeader {
				r.finishArchive(reservedArchiveThreadID, reservedArchive, plan.Result, nil)
			}
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
	var upstreamErr error
	if method == "thread/archive" && source.role == RoleDesktop && !ephemeral {
		var gate func(context.Context) error
		if controlled {
			if controller, ok := r.options.Controller.(ArchiveGate); ok {
				gate = func(gateCtx context.Context) error {
					return controller.WaitArchiveReady(gateCtx, call, plan)
				}
			}
		}
		result, upstreamErr = r.archiveThread(ctx, plan.Params, gate,
			reservedArchive, reservedArchiveLeader)
	} else {
		skipUpstream := false
		if method == "thread/unarchive" && !ephemeral {
			if threadID, _ := threadScope(plan.Params); threadID != "" {
				if r.cancelPendingArchive(threadID) {
					result = json.RawMessage(`{}`)
					skipUpstream = true
				}
			}
		}
		if !skipUpstream {
			upstreamErr = r.upstream.Call(ctx, method, plan.Params, &result)
		}
	}
	if upstreamErr != nil {
		if controlled {
			return r.completeControlled(ctx, call, plan, nil, upstreamErr)
		}
		return nil, upstreamErr
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
		switch event.Method {
		case "turn/started", "turn/completed", "thread/archived", "thread/unarchived":
			r.signalLifecycle(threadID)
		}
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

func (r *Relay) archiveThread(ctx context.Context, params json.RawMessage,
	gate func(context.Context) error, operation *archiveOperation, leader bool,
) (json.RawMessage, error) {
	threadID, _ := threadScope(params)
	if threadID == "" {
		return nil, errors.New("thread/archive 缺少 threadId")
	}
	if operation == nil {
		operation, leader = r.beginArchive(threadID)
	}
	if !leader {
		select {
		case <-operation.done:
			return append(json.RawMessage(nil), operation.result...), operation.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	result, err := r.runArchive(ctx, threadID, params, operation, gate)
	r.finishArchive(threadID, operation, result, err)
	return result, err
}

func (r *Relay) beginArchive(threadID string) (*archiveOperation, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if current := r.archiveOperations[threadID]; current != nil {
		return current, false
	}
	operation := &archiveOperation{done: make(chan struct{}), wake: make(chan struct{}, 1)}
	r.archiveOperations[threadID] = operation
	return operation, true
}

func (r *Relay) runArchive(ctx context.Context, threadID string, params json.RawMessage,
	operation *archiveOperation, gate func(context.Context) error,
) (json.RawMessage, error) {
	for {
		if r.archiveCanceled(operation) {
			return nil, &ProtocolError{Code: -32053,
				Message: "Codex Thread 归档已被恢复请求取消"}
		}
		active, err := r.threadActive(ctx, threadID)
		if err != nil {
			return nil, err
		}
		if !active {
			break
		}
		select {
		case <-operation.wake:
		case <-time.After(time.Second):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if gate != nil {
		if err := gate(ctx); err != nil {
			return nil, err
		}
	}
	if !r.beginArchiveApply(operation) {
		return nil, &ProtocolError{Code: -32053,
			Message: "Codex Thread 归档已被恢复请求取消"}
	}
	var result json.RawMessage
	if err := r.upstream.Call(ctx, "thread/archive", params, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (r *Relay) archiveCanceled(operation *archiveOperation) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return operation.canceled
}

func (r *Relay) beginArchiveApply(operation *archiveOperation) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if operation.canceled {
		return false
	}
	operation.applying = true
	return true
}

func (r *Relay) cancelPendingArchive(threadID string) bool {
	r.mu.Lock()
	operation := r.archiveOperations[threadID]
	canceled := false
	if operation != nil && !operation.applying {
		operation.canceled = true
		canceled = true
	}
	r.mu.Unlock()
	if operation == nil {
		return false
	}
	select {
	case operation.wake <- struct{}{}:
	default:
	}
	return canceled
}

func (r *Relay) threadActive(ctx context.Context, threadID string) (bool, error) {
	var result struct {
		Thread struct {
			Status json.RawMessage `json:"status"`
		} `json:"thread"`
	}
	if err := r.upstream.Call(ctx, "thread/read", map[string]any{
		"threadId": threadID, "includeTurns": false,
	}, &result); err != nil {
		return false, err
	}
	var status struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(result.Thread.Status, &status) != nil {
		return false, nil
	}
	return status.Type == "active", nil
}

func (r *Relay) finishArchive(threadID string, operation *archiveOperation,
	result json.RawMessage, err error,
) {
	r.mu.Lock()
	if r.archiveOperations[threadID] == operation {
		delete(r.archiveOperations, threadID)
	}
	operation.result = append(json.RawMessage(nil), result...)
	operation.err = err
	close(operation.done)
	r.mu.Unlock()
}

func (r *Relay) archivePending(threadID string) bool {
	if threadID == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.archiveOperations[threadID] != nil
}

func (r *Relay) signalLifecycle(threadID string) {
	if threadID == "" {
		return
	}
	r.mu.Lock()
	operation := r.archiveOperations[threadID]
	r.mu.Unlock()
	if operation == nil {
		return
	}
	select {
	case operation.wake <- struct{}{}:
	default:
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
