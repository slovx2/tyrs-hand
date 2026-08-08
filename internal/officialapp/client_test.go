package officialapp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/stretchr/testify/require"
)

type rpcCall struct {
	method string
	params any
}

type scriptedRPC struct {
	calls  []rpcCall
	invoke func(method string, params, result any) error
}

func (c *scriptedRPC) Call(_ context.Context, method string, params, result any) error {
	c.calls = append(c.calls, rpcCall{method: method, params: params})
	return c.invoke(method, params, result)
}

func writeResult(target, value any) {
	encoded, _ := json.Marshal(value)
	_ = json.Unmarshal(encoded, target)
}

func TestSubmitUsesSteerWithExpectedTurnAndDismissesFirst(t *testing.T) {
	dismissed := false
	client := &scriptedRPC{}
	client.invoke = func(method string, _ any, result any) error {
		switch method {
		case "thread/resume":
		case "thread/read":
			writeResult(result, map[string]any{"thread": map[string]any{
				"id": "thread-1", "turns": []any{map[string]any{
					"id": "turn-active", "status": "inProgress", "items": []any{},
				}},
			}})
		case "turn/steer":
			require.True(t, dismissed)
			writeResult(result, map[string]any{"turnId": "turn-active"})
		default:
			t.Fatalf("意外方法 %s", method)
		}
		return nil
	}
	result, err := Submit(context.Background(), client, SubmitRequest{
		ThreadID: "thread-1", ClientMessageID: "message-1",
		Input: []UserInput{TextInput("continue")},
		DismissOutstanding: func(context.Context, string) error {
			dismissed = true
			return nil
		},
	})
	require.NoError(t, err)
	require.Equal(t, "turn-active", result.TurnID)
	params := client.calls[len(client.calls)-1].params.(map[string]any)
	require.Equal(t, "turn-active", params["expectedTurnId"])
	require.Equal(t, "message-1", params["clientUserMessageId"])
}

func TestSubmitRefreshesAndRetriesTurnMismatchOnce(t *testing.T) {
	reads, steers := 0, 0
	client := &scriptedRPC{}
	client.invoke = func(method string, _ any, result any) error {
		switch method {
		case "thread/resume":
			return nil
		case "thread/read":
			reads++
			turnID := "turn-old"
			if reads > 1 {
				turnID = "turn-new"
			}
			writeResult(result, map[string]any{"thread": map[string]any{"id": "thread-1",
				"turns": []any{map[string]any{"id": turnID, "status": "inProgress",
					"items": []any{}}}}})
			return nil
		case "turn/steer":
			steers++
			if steers == 1 {
				return &codex.RequestError{Method: method, State: codex.RequestRejected,
					Cause: &codex.RPCError{Code: -32600,
						Message: "expected active turn id turn-new"}}
			}
			writeResult(result, map[string]any{"turnId": "turn-new"})
			return nil
		}
		return errors.New("意外方法")
	}
	result, err := Submit(context.Background(), client, SubmitRequest{
		ThreadID: "thread-1", ClientMessageID: "message-1",
		Input: []UserInput{TextInput("continue")},
	})
	require.NoError(t, err)
	require.Equal(t, "turn-new", result.TurnID)
	require.Equal(t, 2, reads)
	require.Equal(t, 2, steers)
}

func TestSubmitRefreshesIdleSnapshotAndSteersNewActiveTurn(t *testing.T) {
	reads, dismissals := 0, 0
	client := &scriptedRPC{}
	client.invoke = func(method string, _ any, result any) error {
		switch method {
		case "thread/resume":
			return nil
		case "thread/read":
			reads++
			turns := []any{}
			if reads > 1 {
				turns = []any{map[string]any{"id": "turn-external", "status": "inProgress",
					"items": []any{}}}
			}
			writeResult(result, map[string]any{"thread": map[string]any{
				"id": "thread-1", "turns": turns}})
			return nil
		case "turn/start":
			return &codex.RequestError{Method: method, State: codex.RequestRejected,
				Cause: &codex.RPCError{Code: -32600,
					Message: "thread already has an active turn"}}
		case "turn/steer":
			writeResult(result, map[string]any{"turnId": "turn-external"})
			return nil
		default:
			return errors.New("意外方法")
		}
	}
	result, err := Submit(context.Background(), client, SubmitRequest{
		ThreadID: "thread-1", ClientMessageID: "message-race",
		Input: []UserInput{TextInput("join")},
		DismissOutstanding: func(context.Context, string) error {
			dismissals++
			return nil
		},
	})
	require.NoError(t, err)
	require.Equal(t, "turn-external", result.TurnID)
	require.Equal(t, 2, reads)
	require.Equal(t, 2, dismissals)
}

func TestSubmitDeduplicatesFromOfficialHistory(t *testing.T) {
	client := &scriptedRPC{invoke: func(method string, _ any, result any) error {
		if method == "thread/read" {
			writeResult(result, map[string]any{"thread": map[string]any{"id": "thread-1",
				"turns": []any{map[string]any{"id": "turn-existing", "status": "completed",
					"items": []any{map[string]any{"type": "userMessage", "id": "item-1",
						"clientId": "message-1", "content": []any{}}}}}}})
		}
		return nil
	}}
	result, err := Submit(context.Background(), client, SubmitRequest{
		ThreadID: "thread-1", ClientMessageID: "message-1",
	})
	require.NoError(t, err)
	require.True(t, result.Deduplicated)
	require.Equal(t, "turn-existing", result.TurnID)
	require.Len(t, client.calls, 2)
}

func TestLatestCompletedPlanOnlyUsesLatestCompletedTurn(t *testing.T) {
	thread := Thread{Turns: []Turn{
		{ID: "turn-1", Status: "completed", Items: []Item{{Type: "plan", ID: "plan-1", Text: "old"}}},
		{ID: "turn-2", Status: "completed", Items: []Item{{Type: "agentMessage", ID: "answer"}}},
		{ID: "turn-3", Status: "inProgress", Items: []Item{{Type: "plan", ID: "plan-3", Text: "draft"}}},
	}}
	require.Nil(t, thread.LatestCompletedPlan())
	thread.Turns = thread.Turns[:1]
	require.Equal(t, &Plan{TurnID: "turn-1", ItemID: "plan-1", Text: "old"},
		thread.LatestCompletedPlan())
}
