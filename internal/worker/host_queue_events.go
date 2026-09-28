package worker

import (
	"encoding/json"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"go.uber.org/zap"
)

func (c *HostDesktopController) observeHostQueue(threadID string, state *hostQueueState) {
	for {
		select {
		case <-c.processor.workspaces.ctx.Done():
			c.mu.Lock()
			c.closeHostQueueLocked(threadID, state)
			c.mu.Unlock()
			return
		case event, ok := <-state.subscription.Events():
			if !ok {
				// 丢失通知不等于执行结束。保留槽直到进程换代，避免未知回合超配。
				c.mu.Lock()
				if !state.closed {
					c.processor.logger.Warn("原生队列事件流中断，保留执行槽等待运行时恢复", zap.String("thread_id", threadID))
					c.closeQueueExecutionEventsLocked(state)
				}
				c.mu.Unlock()
				return
			}
			c.applyHostQueueEvent(threadID, state, event)
		}
	}
}

func (c *HostDesktopController) applyHostQueueEvent(threadID string, state *hostQueueState, event codex.Event) {
	var params struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		Item     struct {
			Type     string          `json:"type"`
			ClientID string          `json:"clientId"`
			Content  json.RawMessage `json:"content"`
		} `json:"item"`
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if json.Unmarshal(event.Params, &params) != nil || params.ThreadID != threadID {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if state.closed {
		return
	}
	switch event.Method {
	case "item/started", "item/completed":
		if params.Item.Type != "userMessage" || params.Item.ClientID == "" || params.TurnID == "" {
			break
		}
		state.observed[params.Item.ClientID] = params.TurnID
		if state.completed[params.TurnID] {
			return
		}
		item := state.items[params.Item.ClientID]
		if item == nil {
			if c.active[threadID] != nil {
				if state.foreign != nil {
					state.foreign[params.TurnID] = true
				}
				c.notifyQueueChangedLocked(state)
				return
			}
			// 初始化分页期间已被原生取出的项仍在执行，不能因 list 缺席而漏记。
			item = &hostQueueItem{ClientID: params.Item.ClientID}
			state.items[item.ClientID] = item
		}
		if item.turnID == "" {
			item.turnID = params.TurnID
		}
		c.startQueueExecutionLocked(threadID, state, item, params.Item.Content)
	case "turn/completed":
		if params.Turn.ID == "" {
			return
		}
		state.completed[params.Turn.ID] = true
		for clientID, item := range state.items {
			if item.turnID == params.Turn.ID {
				delete(state.items, clientID)
			}
		}
	}
	turnID := params.TurnID
	if turnID == "" {
		turnID = params.Turn.ID
	}
	if execution := state.turns[turnID]; execution != nil && !execution.closed {
		select {
		case execution.events <- event:
		default:
			execution.closed = true
			close(execution.events)
			c.processor.logger.Error("原生队列回合事件积压，转入原生快照对账", zap.String("thread_id", threadID), zap.String("turn_id", turnID))
		}
	}
	c.notifyQueueChangedLocked(state)
	c.finishHostQueueIfIdleLocked(threadID, state)
}

func (c *HostDesktopController) notifyQueueChangedLocked(state *hostQueueState) {
	if state.changed != nil {
		close(state.changed)
		state.changed = make(chan struct{})
	}
}

func (c *HostDesktopController) closeQueueExecutionEventsLocked(state *hostQueueState) {
	for _, execution := range state.turns {
		if !execution.closed {
			execution.closed = true
			close(execution.events)
		}
	}
}

func (c *HostDesktopController) finishHostQueueIfIdleLocked(threadID string, state *hostQueueState) {
	if state.inflight == 0 && len(state.items) == 0 {
		c.closeHostQueueLocked(threadID, state)
	}
}

func (c *HostDesktopController) closeHostQueueLocked(threadID string, state *hostQueueState) {
	if state.closed {
		return
	}
	state.closed = true
	c.notifyQueueChangedLocked(state)
	c.closeQueueExecutionEventsLocked(state)
	if c.queued[threadID] == state {
		delete(c.queued, threadID)
	}
	if state.subscription != nil {
		state.subscription.Close()
	}
	state.slot.release()
}
