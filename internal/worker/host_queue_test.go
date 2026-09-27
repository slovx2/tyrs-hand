package worker

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/appserverhub"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/stretchr/testify/require"
)

func newQueueBudgetTest(t *testing.T, clientIDs ...string) (*HostDesktopController, *hostQueueState) {
	t.Helper()
	c := &HostDesktopController{processor: &Processor{turnSlots: make(chan struct{}, 1)},
		active: make(map[string]*hostCallState), queued: make(map[string]*hostQueueState)}
	slot, err := c.reserveExecutionSlot("thread")
	require.NoError(t, err)
	state := &hostQueueState{slot: slot, items: make(map[string]*hostQueueItem),
		observed: make(map[string]string), completed: make(map[string]bool), deleted: make(map[string]bool), ready: make(chan struct{})}
	close(state.ready)
	for _, id := range clientIDs {
		state.items[id] = &hostQueueItem{ClientID: id, ID: "queued-" + id}
	}
	c.queued["thread"] = state
	return c, state
}

func queueTestEvent(t *testing.T, c *HostDesktopController, state *hostQueueState, method, turnID, clientID string) {
	t.Helper()
	params, err := json.Marshal(map[string]any{"threadId": "thread", "turnId": turnID,
		"turn": map[string]string{"id": turnID}, "item": map[string]string{"type": "userMessage", "clientId": clientID}})
	require.NoError(t, err)
	c.applyHostQueueEvent("thread", state, codex.Event{Method: method, Params: params})
}

func TestHostQueueSharesSlotUntilEveryNativeTurnCompletes(t *testing.T) {
	c, state := newQueueBudgetTest(t, "one", "two")
	slot, err := c.reserveExecutionSlot("thread")
	require.NoError(t, err)
	direct := &hostCallState{slot: slot}
	c.active["thread"] = direct
	require.Len(t, c.processor.turnSlots, 1)
	c.finishHostCall("thread", direct)
	queueTestEvent(t, c, state, "turn/completed", "direct", "")
	require.Len(t, state.items, 2, "其他回合完成不能误认队列已结束")
	queueTestEvent(t, c, state, "item/started", "turn-one", "one")
	queueTestEvent(t, c, state, "turn/completed", "turn-one", "")
	require.Len(t, c.processor.turnSlots, 1, "下一队列条目仍持有共享槽")
	_, err = c.reserveExecutionSlot("another-thread")
	require.ErrorContains(t, err, "并发上限")
	queueTestEvent(t, c, state, "item/completed", "turn-two", "two")
	queueTestEvent(t, c, state, "turn/completed", "turn-two", "")
	require.Empty(t, c.processor.turnSlots)
	require.Empty(t, c.queued)
	queueTestEvent(t, c, state, "turn/completed", "turn-two", "")
	c.finishHostCall("thread", direct)
	require.Empty(t, c.processor.turnSlots, "重复结束不能归还两次")
}

func TestHostQueueRetainsSlotUntilAddResponseAfterFastTurn(t *testing.T) {
	c, state := newQueueBudgetTest(t, "one")
	callState := &hostQueueCall{state: state, added: state.items["one"]}
	state.inflight = 1
	queueTestEvent(t, c, state, "item/started", "turn", "one")
	queueTestEvent(t, c, state, "turn/completed", "turn", "")
	require.Empty(t, state.items)
	require.Len(t, c.processor.turnSlots, 1)
	call := appserverhub.Call{Method: "thread/queue/add", Params: json.RawMessage(`{"threadId":"thread"}`)}
	result := json.RawMessage(`{"queuedSubmission":{"id":"queued-one","clientUserMessageId":"one"}}`)
	c.completeQueueCall(call, callState, result, nil)
	c.completeQueueCall(call, callState, result, nil)
	require.Zero(t, state.inflight)
	require.Empty(t, c.processor.turnSlots)
	require.Empty(t, state.items, "迟到响应不能把已结束条目放回队列")
}

func TestHostQueueDeleteRequiresConfirmedRemoval(t *testing.T) {
	for _, test := range []struct {
		name    string
		started bool
		result  string
		freed   bool
	}{
		{"已被原生取走", false, `{"deleted":false}`, false},
		{"响应格式未知", false, `{}`, false},
		{"明确删除等待项", false, `{"deleted":true}`, true},
		{"不能撤销已执行回合", true, `{"deleted":true}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, state := newQueueBudgetTest(t, "one")
			state.inflight = 1
			if test.started {
				queueTestEvent(t, c, state, "item/started", "turn", "one")
			}
			call := appserverhub.Call{Method: "thread/queue/delete", Params: json.RawMessage(`{"threadId":"thread"}`)}
			c.completeQueueCall(call, &hostQueueCall{state: state, deleteID: "queued-one"}, json.RawMessage(test.result), nil)
			require.Equal(t, test.freed, state.closed)
			if !test.freed {
				require.Len(t, c.processor.turnSlots, 1)
			}
		})
	}
}

func TestHostQueueUnknownAddDoesNotReleaseOrReplay(t *testing.T) {
	for _, outcome := range []codex.RequestState{codex.RequestNotSent, codex.RequestRejected, codex.RequestUnknown} {
		t.Run(string(outcome), func(t *testing.T) {
			c, state := newQueueBudgetTest(t, "one")
			state.inflight = 1
			call := appserverhub.Call{Method: "thread/queue/add", Params: json.RawMessage(`{"threadId":"thread"}`)}
			c.completeQueueCall(call, &hostQueueCall{state: state, added: state.items["one"]}, nil,
				&codex.RequestError{State: outcome, Cause: errors.New("测试传输故障")})
			if outcome == codex.RequestUnknown {
				require.Len(t, c.processor.turnSlots, 1)
				queueTestEvent(t, c, state, "item/started", "turn", "one")
				queueTestEvent(t, c, state, "turn/completed", "turn", "")
			}
			require.Empty(t, c.processor.turnSlots)
		})
	}
}

func TestHostQueueDeletionCanArriveBeforeAddResponse(t *testing.T) {
	c, state := newQueueBudgetTest(t, "one")
	item := state.items["one"]
	item.ID = ""
	state.inflight = 2
	params := json.RawMessage(`{"threadId":"thread"}`)
	c.completeQueueCall(appserverhub.Call{Method: "thread/queue/delete", Params: params},
		&hostQueueCall{state: state, deleteID: "queued-one"}, json.RawMessage(`{"deleted":true}`), nil)
	require.Len(t, c.processor.turnSlots, 1, "入队响应仍在途时不能释放")
	c.completeQueueCall(appserverhub.Call{Method: "thread/queue/add", Params: params},
		&hostQueueCall{state: state, added: item},
		json.RawMessage(`{"queuedSubmission":{"id":"queued-one","clientUserMessageId":"one"}}`), nil)
	require.Empty(t, state.items)
	require.Empty(t, c.processor.turnSlots, "删除成功不能因迟到的入队响应永久泄漏槽")
}

func TestHostQueueTracksDispatchedItemAbsentFromList(t *testing.T) {
	c, state := newQueueBudgetTest(t)
	state.inflight = 1
	queueTestEvent(t, c, state, "item/started", "turn", "one")
	c.completeQueueCall(appserverhub.Call{Method: "thread/resume", Params: json.RawMessage(`{"threadId":"thread"}`)},
		&hostQueueCall{state: state}, json.RawMessage(`{}`), nil)
	require.Len(t, c.processor.turnSlots, 1, "分页已取出的条目仍要等待回合终态")
	queueTestEvent(t, c, state, "turn/completed", "turn", "")
	require.Empty(t, c.processor.turnSlots)
}
