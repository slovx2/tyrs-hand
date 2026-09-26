package appserverhub

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/slovx2/tyrs-hand/internal/codex"
)

type pendingReviewStart struct {
	parentID string
	threadID string
	done     chan struct{}
}

type heldReviewEvent struct {
	event codex.Event
	waits []*pendingReviewStart
}

func (r *Hub) startReview(ctx context.Context, source *session, params json.RawMessage,
	ephemeral bool,
) (json.RawMessage, error) {
	parentID, _ := threadScope(params)
	pending := r.beginReviewStart(parentID)
	defer r.finishReviewStart(pending)
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
	pending.threadID = threadID
	return result, nil
}

func (r *Hub) beginReviewStart(parentID string) *pendingReviewStart {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reviewStarts == nil {
		r.reviewStarts = make(map[*pendingReviewStart]bool)
	}
	pending := &pendingReviewStart{parentID: parentID, done: make(chan struct{})}
	r.reviewStarts[pending] = true
	return pending
}

func (r *Hub) finishReviewStart(pending *pendingReviewStart) {
	r.mu.Lock()
	delete(r.reviewStarts, pending)
	close(pending.done)
	r.signalInteractionChangeLocked()
	r.mu.Unlock()
	select {
	case r.reviewChanged <- struct{}{}:
	default:
	}
}

// 调用方持有 Hub 锁。响应前 detached 子 ID 未知，只暂存相关父会话及
// 尚无订阅/回合身份的新会话；既有无关会话继续分发，不阻塞 upstream reader。
func (r *Hub) reviewWaitsLocked(threadID string) []*pendingReviewStart {
	if threadID == "" || len(r.reviewStarts) == 0 {
		return nil
	}
	known := r.toolThreads[threadID] != nil
	for _, source := range r.sessions {
		known = known || source.subscribed(threadID)
	}
	var waits []*pendingReviewStart
	for pending := range r.reviewStarts {
		if !known || pending.parentID == threadID {
			waits = append(waits, pending)
		}
	}
	return waits
}

func (r *Hub) reviewEventWaits(event codex.Event, held []heldReviewEvent) []*pendingReviewStart {
	threadID, _ := threadScope(event.Params)
	for _, queued := range held {
		queuedID, _ := threadScope(queued.event.Params)
		if queuedID == threadID {
			return queued.waits
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reviewWaitsLocked(threadID)
}

func (r *Hub) flushReviewEvents(held []heldReviewEvent) []heldReviewEvent {
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
			r.forwardEvent(queued.event)
		} else {
			remaining = append(remaining, queued)
		}
	}
	return remaining
}
