package appserverhub

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/slovx2/tyrs-hand/internal/codex"
)

type pendingThreadStart struct {
	parentID  string
	threadID  string
	done      chan struct{}
	ephemeral bool
}

type heldThreadEvent struct {
	event codex.Event
	waits []*pendingThreadStart
}

func (r *Hub) startReview(ctx context.Context, source *session, params json.RawMessage,
	ephemeral bool,
) (json.RawMessage, error) {
	parentID, _ := threadScope(params)
	pending := r.beginThreadStart(parentID, ephemeral)
	defer r.finishThreadStart(pending)
	// 即便内部调用者未设置 deadline，也不能长期扣留尚未关联的子会话事件。
	ctx, cancel := requestContext(ctx, r.options.RequestTimeout)
	defer cancel()
	var result json.RawMessage
	if err := r.upstream.Call(ctx, "review/start", params, &result); err != nil {
		return nil, err
	}
	var response struct {
		ThreadID string `json:"reviewThreadId"`
		Turn     struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(result, &response); err != nil || response.ThreadID == "" || response.Turn.ID == "" {
		return nil, fmt.Errorf("review/start 没有返回有效的 reviewThreadId 与 turn.id")
	}
	threadID := response.ThreadID
	// 在解除事件屏障前绑定精确回合的工具执行端，不能由最早连接的旁观端抢占。
	toolTurn := r.beginToolTurn(source, threadID)
	r.finishToolTurnStart(threadID, toolTurn, result, nil)
	if ephemeral {
		r.markEphemeral(threadID)
	}
	if threadID != parentID {
		r.subscribeCreatedThread(source, threadID, ephemeral)
	} else if source.role == RoleDesktop {
		source.subscribe(threadID)
	}
	r.signalInteractionChange()
	r.classifyThreadPrivacy(threadID, ephemeral)
	r.rememberThread(threadID)
	pending.threadID = threadID
	return result, nil
}

func (r *Hub) beginThreadStart(parentID string, ephemeral bool) *pendingThreadStart {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.threadStarts == nil {
		r.threadStarts = make(map[*pendingThreadStart]bool)
	}
	pending := &pendingThreadStart{parentID: parentID, done: make(chan struct{}), ephemeral: ephemeral}
	r.threadStarts[pending] = true
	return pending
}

func (r *Hub) finishThreadStart(pending *pendingThreadStart) {
	r.mu.Lock()
	delete(r.threadStarts, pending)
	if pending.ephemeral && pending.threadID == "" {
		// 取消不取消原生创建；迟到事件的 ID 仍未知，后续必须先取得明确的隐私分类。
		r.unclassifiedThreadGuard = true
	}
	close(pending.done)
	r.signalInteractionChangeLocked()
	r.mu.Unlock()
	select {
	case r.threadStartsChanged <- struct{}{}:
	default:
	}
}

// 调用方持有 Hub 锁。创建、分叉及独立审查的响应前新 ID 未知，只暂存相关
// 原会话及尚无订阅/回合身份的新会话；既有无关会话继续分发，不阻塞 reader。
func (r *Hub) threadStartWaitsLocked(threadID string) []*pendingThreadStart {
	if threadID == "" || len(r.threadStarts) == 0 {
		return nil
	}
	known := r.knownThreads[threadID] || r.toolThreads[threadID] != nil
	for _, source := range r.sessions {
		known = known || source.subscribed(threadID)
	}
	var waits []*pendingThreadStart
	for pending := range r.threadStarts {
		if !known || pending.parentID == threadID {
			waits = append(waits, pending)
		}
	}
	return waits
}

// Thread 身份独立于 Desktop 订阅和活动工具回合，Worker 隐式消费的会话也要保留。
func (r *Hub) rememberThread(threadID string) {
	if threadID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.knownThreads == nil {
		r.knownThreads = make(map[string]bool)
	}
	r.knownThreads[threadID] = true
}

func (r *Hub) threadEventWaits(event codex.Event, held []heldThreadEvent) []*pendingThreadStart {
	threadID, _ := threadScope(event.Params)
	for _, queued := range held {
		queuedID, _ := threadScope(queued.event.Params)
		if queuedID == threadID {
			return queued.waits
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.threadStartWaitsLocked(threadID)
}

func (r *Hub) flushThreadEvents(held []heldThreadEvent) []heldThreadEvent {
	remaining := held[:0]
	for _, queued := range held {
		ready := true
		matched := false
		threadID, _ := threadScope(queued.event.Params)
		for _, wait := range queued.waits {
			select {
			case <-wait.done:
				matched = matched || wait.threadID == threadID
			default:
				ready = false
			}
		}
		if ready || matched {
			r.forwardOrQuarantine(queued.event, queued.waits)
		} else {
			remaining = append(remaining, queued)
		}
	}
	return remaining
}
