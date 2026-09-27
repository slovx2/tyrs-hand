package worker

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/slovx2/tyrs-hand/internal/appserverhub"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/hostworker"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
)

type hostQueueItem struct {
	ID       string `json:"id"`
	ClientID string `json:"clientUserMessageId"`
	turnID   string
}

// 所有可变字段由 Controller.mu 保护；RPC 在途时不提前归还槽。
type hostQueueState struct {
	slot         *hostExecutionSlot
	subscription *appserverhub.Subscription
	items        map[string]*hostQueueItem
	observed     map[string]string
	completed    map[string]bool
	deleted      map[string]bool
	ready        chan struct{}
	inflight     int
	closed       bool
}

type hostQueueCall struct {
	state    *hostQueueState
	added    *hostQueueItem
	deleteID string
	once     sync.Once
}

func (c *HostDesktopController) prepareQueueCall(ctx context.Context, runtime *hostworker.Runtime, call appserverhub.Call) (*hostQueueCall, error) {
	switch call.Method {
	case "thread/queue/add", "thread/queue/delete", "thread/queue/start", "thread/resume":
	default:
		return nil, nil
	}
	if runtime == nil {
		return nil, errors.New("宿主 Codex Runtime 正在恢复")
	}
	client := runtime.Client()
	if client == nil {
		return nil, errors.New("宿主 Codex Runtime 正在恢复")
	}
	if runtime.Info().Engine != runtimeidentity.Codex {
		return nil, nil
	}
	var input struct {
		ThreadID string `json:"threadId"`
		ClientID string `json:"clientUserMessageId"`
		DeleteID string `json:"queuedSubmissionId"`
	}
	if err := json.Unmarshal(call.Params, &input); err != nil {
		return nil, err
	}
	if input.ThreadID == "" {
		return nil, nil
	}
	if call.Method == "thread/queue/add" && input.ClientID == "" {
		return nil, errors.New("队列输入缺少 clientUserMessageId")
	}
	queueCall, err := c.beginHostQueueCall(ctx, client, input.ThreadID)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state := queueCall.state
	if state.closed {
		state.inflight--
		return nil, errors.New("原生队列运行时已换代")
	}
	if state.slot == nil && (len(state.items) > 0 || call.Method == "thread/queue/add") {
		slot, slotErr := c.reserveExecutionSlot(input.ThreadID)
		if slotErr != nil {
			state.inflight--
			c.closeHostQueueLocked(input.ThreadID, state)
			return nil, slotErr
		}
		state.slot = slot
	}
	queueCall.deleteID = input.DeleteID
	if call.Method == "thread/queue/add" {
		if state.items[input.ClientID] != nil {
			state.inflight--
			c.finishHostQueueIfIdleLocked(input.ThreadID, state)
			return nil, errors.New("队列 clientUserMessageId 已在等待或执行中")
		}
		queueCall.added = &hostQueueItem{ClientID: input.ClientID}
		state.items[input.ClientID] = queueCall.added
	}
	return queueCall, nil
}

func (c *HostDesktopController) beginHostQueueCall(ctx context.Context, client *appserverhub.Client, threadID string) (*hostQueueCall, error) {
	for {
		c.mu.Lock()
		if state := c.queued[threadID]; state != nil {
			select {
			case <-state.ready:
				state.inflight++
				c.mu.Unlock()
				return &hostQueueCall{state: state}, nil
			default:
				c.mu.Unlock()
				select {
				case <-state.ready:
					continue
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
		}
		// 初始化期间也先占用状态，事件和并发 RPC 不能把旧 list 条目复活。
		state := &hostQueueState{subscription: client.Subscribe(codex.ThreadFilter{ThreadID: threadID}),
			items: make(map[string]*hostQueueItem), observed: make(map[string]string), completed: make(map[string]bool),
			deleted: make(map[string]bool),
			ready:   make(chan struct{}), inflight: 1}
		c.queued[threadID] = state
		c.mu.Unlock()
		go c.observeHostQueue(threadID, state)
		items, err := listHostQueue(ctx, client, threadID)
		c.mu.Lock()
		if err == nil && state.closed {
			err = errors.New("读取队列期间运行时已换代")
		}
		if err == nil {
			for index := range items {
				item := &items[index]
				item.turnID = state.observed[item.ClientID]
				if !state.completed[item.turnID] {
					state.items[item.ClientID] = item
				}
			}
		} else {
			state.inflight--
			c.closeHostQueueLocked(threadID, state)
		}
		close(state.ready)
		c.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return &hostQueueCall{state: state}, nil
	}
}

func listHostQueue(ctx context.Context, client *appserverhub.Client, threadID string) ([]hostQueueItem, error) {
	var items []hostQueueItem
	cursor := ""
	seen := make(map[string]bool)
	for {
		params := map[string]any{"threadId": threadID, "limit": 50}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var result struct {
			Data       []hostQueueItem `json:"data"`
			NextCursor *string         `json:"nextCursor"`
		}
		if err := client.Call(ctx, "thread/queue/list", params, &result); err != nil {
			return nil, err
		}
		items = append(items, result.Data...)
		if result.NextCursor == nil {
			return items, nil
		}
		cursor = *result.NextCursor
		if cursor == "" || seen[cursor] {
			return nil, errors.New("原生队列分页游标未前进")
		}
		seen[cursor] = true
	}
}

func (c *HostDesktopController) completeQueueCall(call appserverhub.Call, queueCall *hostQueueCall, result json.RawMessage, cause error) {
	if queueCall == nil {
		return
	}
	queueCall.once.Do(func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		state := queueCall.state
		state.inflight--
		if state.closed {
			return
		}
		threadID, _ := callScope(call.Params)
		if item := queueCall.added; item != nil {
			var requestErr *codex.RequestError
			if errors.As(cause, &requestErr) && (requestErr.State == codex.RequestNotSent || requestErr.State == codex.RequestRejected) {
				if state.items[item.ClientID] == item && item.turnID == "" {
					delete(state.items, item.ClientID)
				}
			} else if cause == nil {
				var response struct {
					Item hostQueueItem `json:"queuedSubmission"`
				}
				if json.Unmarshal(result, &response) == nil && response.Item.ClientID == item.ClientID {
					item.ID = response.Item.ID
					if state.deleted[item.ID] && item.turnID == "" {
						delete(state.items, item.ClientID)
					}
				}
			}
		}
		if call.Method == "thread/queue/delete" && cause == nil {
			var response struct {
				Deleted bool `json:"deleted"`
			}
			if json.Unmarshal(result, &response) == nil && response.Deleted {
				state.deleted[queueCall.deleteID] = true
				for clientID, item := range state.items {
					if item.ID == queueCall.deleteID && item.turnID == "" {
						delete(state.items, clientID)
					}
				}
			}
		}
		c.finishHostQueueIfIdleLocked(threadID, state)
	})
}
