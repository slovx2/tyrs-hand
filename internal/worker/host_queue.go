package worker

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/slovx2/tyrs-hand/internal/appserverhub"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/hostworker"
	"github.com/slovx2/tyrs-hand/internal/participantidentity"
	"go.uber.org/zap"
)

type hostQueueItem struct {
	ID         string          `json:"id"`
	ClientID   string          `json:"clientUserMessageId"`
	Input      json.RawMessage `json:"input"`
	turnID     string
	journal    *hostQueueJournal
	controller *desktopController
	execution  *hostQueueExecution
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
	changed      chan struct{}
	turns        map[string]*hostQueueExecution
	foreign      map[string]bool
}

type hostQueueCall struct {
	state    *hostQueueState
	added    *hostQueueItem
	deleteID string
	once     sync.Once
	params   json.RawMessage
}

func (c *HostDesktopController) prepareQueueCall(ctx context.Context, runtime *hostworker.Runtime, integration *desktopController, call appserverhub.Call) (*hostQueueCall, error) {
	switch call.Method {
	case "thread/queue/add", "thread/queue/delete", "thread/queue/update", "thread/queue/start", "thread/resume":
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
	if runtime.Info().Engine != c.processor.client.Engine() {
		return nil, errors.New("队列运行时与 Worker 引擎不一致")
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
	state := queueCall.state
	if state.closed {
		state.inflight--
		c.mu.Unlock()
		return nil, errors.New("原生队列运行时已换代")
	}
	if state.slot == nil && (len(state.items) > 0 || call.Method == "thread/queue/add") {
		slot, slotErr := c.reserveExecutionSlot(input.ThreadID)
		if slotErr != nil {
			state.inflight--
			c.closeHostQueueLocked(input.ThreadID, state)
			c.mu.Unlock()
			return nil, slotErr
		}
		state.slot = slot
	}
	queueCall.deleteID = input.DeleteID
	if call.Method == "thread/queue/update" {
		queueCall.params = participantidentity.StripTurnContext(call.Params)
	}
	if call.Method == "thread/queue/add" {
		if state.items[input.ClientID] != nil {
			state.inflight--
			c.finishHostQueueIfIdleLocked(input.ThreadID, state)
			c.mu.Unlock()
			return nil, errors.New("队列 clientUserMessageId 已在等待或执行中")
		}
		queueCall.added = &hostQueueItem{ClientID: input.ClientID}
		state.items[input.ClientID] = queueCall.added
	}
	c.mu.Unlock()
	if queueCall.added != nil {
		queueCall.params, err = c.captureQueueInput(ctx, runtime, integration, call, queueCall.added)
		if err != nil {
			c.completeQueueCall(call, queueCall, nil, &codex.RequestError{State: codex.RequestNotSent, Cause: err})
			return nil, err
		}
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
			ready:   make(chan struct{}), changed: make(chan struct{}), turns: make(map[string]*hostQueueExecution), foreign: make(map[string]bool), inflight: 1}
		c.queued[threadID] = state
		c.mu.Unlock()
		go c.observeHostQueue(threadID, state)
		items, err := listHostQueue(ctx, client, threadID)
		if err == nil && c.processor.journals != nil {
			_, runtime := c.snapshot()
			for index := range items {
				items[index].journal, err = c.processor.journals.readQueue(threadID, items[index].ClientID)
				if err != nil {
					break
				}
				if items[index].journal != nil {
					if items[index].journal.Engine != runtime.Info().Engine {
						err = errors.New("队列 Journal 与运行时引擎不一致")
						break
					}
					items[index].journal, err = items[index].journal.withInput(items[index].Input)
					if err == nil {
						err = c.processor.journals.saveQueue(items[index].journal)
					}
					if err != nil {
						break
					}
				}
				items[index].controller = c.queueController(items[index].journal, runtime)
			}
		}
		c.mu.Lock()
		if err == nil && state.closed {
			err = errors.New("读取队列期间运行时已换代")
		}
		if err == nil {
			for index := range items {
				item := &items[index]
				item.turnID = state.observed[item.ClientID]
				if !state.completed[item.turnID] {
					if state.items[item.ClientID] == nil {
						state.items[item.ClientID] = item
					}
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
		if call.Method == "thread/queue/update" && cause == nil {
			var response struct {
				Item hostQueueItem `json:"queuedSubmission"`
			}
			if json.Unmarshal(result, &response) == nil {
				item := state.items[response.Item.ClientID]
				if item != nil && item.ID == queueCall.deleteID && item.turnID == "" && item.journal != nil {
					updated, err := item.journal.withInput(response.Item.Input)
					if err == nil {
						err = c.processor.journals.saveQueue(updated)
					}
					if err != nil {
						c.processor.logger.Error("保存原生队列编辑结果失败，保留身份等待实际输入对账", zap.Error(err))
					} else {
						item.journal = updated
					}
				}
			}
		}
		if item := queueCall.added; item != nil {
			var requestErr *codex.RequestError
			if errors.As(cause, &requestErr) && (requestErr.State == codex.RequestNotSent || requestErr.State == codex.RequestRejected) {
				if state.items[item.ClientID] == item && item.turnID == "" {
					c.removeQueueJournalLocked(threadID, item)
					delete(state.items, item.ClientID)
				}
			} else if cause == nil {
				var response struct {
					Item hostQueueItem `json:"queuedSubmission"`
				}
				if json.Unmarshal(result, &response) == nil && response.Item.ClientID == item.ClientID {
					item.ID = response.Item.ID
					if state.deleted[item.ID] && item.turnID == "" {
						c.removeQueueJournalLocked(threadID, item)
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
						c.removeQueueJournalLocked(threadID, item)
						delete(state.items, clientID)
					}
				}
			}
		}
		c.finishHostQueueIfIdleLocked(threadID, state)
	})
}
