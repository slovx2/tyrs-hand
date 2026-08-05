package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/stretchr/testify/require"
)

type titleCaller struct {
	method string
	params map[string]any
}

func (c *titleCaller) Call(_ context.Context, method string, params, result any) error {
	c.method = method
	c.params = params.(map[string]any)
	var response any
	if method == "thread/start" {
		response = map[string]any{"thread": map[string]any{"id": "title-thread"}}
	} else {
		response = map[string]any{"turn": map[string]any{"id": "title-turn"}}
	}
	encoded, _ := json.Marshal(response)
	return json.Unmarshal(encoded, result)
}

func TestSessionTitleThreadIsEphemeralAndRestricted(t *testing.T) {
	caller := &titleCaller{}
	threadID, err := startSessionTitleThread(context.Background(), caller, t.TempDir())
	require.NoError(t, err)
	require.Equal(t, "title-thread", threadID)
	require.Equal(t, "thread/start", caller.method)
	require.Equal(t, sessionTitleModel, caller.params["model"])
	require.Equal(t, true, caller.params["ephemeral"])
	require.Equal(t, "never", caller.params["approvalPolicy"])
	require.Equal(t, "read-only", caller.params["sandbox"])
	require.Empty(t, caller.params["dynamicTools"])
	require.Equal(t, false, caller.params["config"].(map[string]any)["default_tools_enabled"])
}

func TestSessionTitleTurnUsesStructuredOutput(t *testing.T) {
	caller := &titleCaller{}
	turnID, err := startSessionTitleTurn(context.Background(), caller, "title-thread", "测试任务")
	require.NoError(t, err)
	require.Equal(t, "title-turn", turnID)
	require.Equal(t, "turn/start", caller.method)
	require.Equal(t, sessionTitleModel, caller.params["model"])
	require.Equal(t, "low", caller.params["effort"])
	schema := caller.params["outputSchema"].(map[string]any)
	require.Equal(t, "object", schema["type"])
	properties := schema["properties"].(map[string]any)
	require.Equal(t, sessionTitleMaxRunes,
		properties["title"].(map[string]any)["maxLength"])
}

func TestNormalizeGeneratedSessionTitle(t *testing.T) {
	require.Equal(t, "测试 标题", normalizeGeneratedSessionTitle("  测试\n标题  "))
	require.Len(t, []rune(normalizeGeneratedSessionTitle("一二三四五六七八九十一二三四五六七八九十"+
		"一二三四五六七八九十一二三四五六七八九十")), sessionTitleMaxRunes)
}

func TestWaitSessionTitleTurnUsesCompletedItemForEphemeralThread(t *testing.T) {
	events := make(chan codex.Event, 2)
	events <- codex.Event{Method: "item/completed", Params: json.RawMessage(
		`{"threadId":"title-thread","turnId":"title-turn","item":{` +
			`"type":"agentMessage","phase":"final_answer","text":"{\"title\":\"真实 Luna 标题\"}"}}`)}
	events <- codex.Event{Method: "turn/completed", Params: json.RawMessage(
		`{"threadId":"title-thread","turn":{"id":"title-turn","status":"completed"}}`)}

	output, err := waitSessionTitleTurn(context.Background(), events, "title-thread", "title-turn")
	require.NoError(t, err)
	require.JSONEq(t, `{"title":"真实 Luna 标题"}`, output)
}

func TestWaitSessionTitleTurnRejectsCompletedTurnWithoutOutput(t *testing.T) {
	events := make(chan codex.Event, 1)
	events <- codex.Event{Method: "turn/completed", Params: json.RawMessage(
		`{"threadId":"title-thread","turn":{"id":"title-turn","status":"completed"}}`)}

	_, err := waitSessionTitleTurn(context.Background(), events, "title-thread", "title-turn")
	require.ErrorContains(t, err, "没有最终输出")
}

func TestWaitSessionTitleTurnErrors(t *testing.T) {
	t.Run("event stream closed", func(t *testing.T) {
		events := make(chan codex.Event)
		close(events)
		_, err := waitSessionTitleTurn(context.Background(), events, "thread", "turn")
		require.ErrorContains(t, err, "事件流已关闭")
	})

	t.Run("context canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := waitSessionTitleTurn(ctx, make(chan codex.Event), "thread", "turn")
		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("terminal failure", func(t *testing.T) {
		events := make(chan codex.Event, 2)
		events <- codex.Event{Method: "turn/completed", Params: json.RawMessage(
			`{"threadId":"other","turn":{"id":"turn","status":"completed"}}`)}
		events <- codex.Event{Method: "turn/completed", Params: json.RawMessage(
			`{"threadId":"thread","turn":{"id":"turn","status":"failed"}}`)}
		_, err := waitSessionTitleTurn(context.Background(), events, "thread", "turn")
		require.ErrorContains(t, err, "终态为 failed")
	})
}

func TestSessionTitleErrorCode(t *testing.T) {
	require.Equal(t, "timeout", sessionTitleErrorCode(context.DeadlineExceeded))
	require.Equal(t, "invalid_output", sessionTitleErrorCode(errors.New("luna 标题不符合结构化输出")))
	require.Equal(t, "invalid_output", sessionTitleErrorCode(errors.New("luna 标题为空")))
	require.Equal(t, "generation_failed", sessionTitleErrorCode(errors.New("upstream unavailable")))
}
