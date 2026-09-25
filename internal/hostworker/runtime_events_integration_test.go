//go:build integration

package hostworker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
)

// EVENTS-003：同一套断言经过两个真实 SSH 入口，不以模型正常返回代替事件验收。
func verifyRuntimeTurnEvents(t *testing.T, ctx context.Context, client *codex.SocketClient,
	subscription *codex.EventSubscription, engine runtimeidentity.Engine, threadID, turnID string,
) {
	t.Helper()
	started, completed := false, false
	active, idle, usageSeen := false, false, false
	itemStarts, itemEnds := map[string]bool{}, map[string]bool{}
	var finalText, streamedText strings.Builder
	for !completed {
		select {
		case <-ctx.Done():
			t.Fatal("真实模型事件链路超时")
		case event, ok := <-subscription.Events():
			require.True(t, ok, "终态前订阅不能关闭")
			var params struct {
				ThreadID, TurnID, Delta string
				Status                  struct{ Type string }
				Turn                    struct {
					ID, Status string
					Error      any
				}
				Item       struct{ ID, Type, Text string }
				TokenUsage struct {
					Last, Total struct{ TotalTokens, InputTokens, OutputTokens int }
				}
			}
			require.NoError(t, json.Unmarshal(event.Params, &params))
			require.Equal(t, threadID, params.ThreadID, "事件不能跨线程")
			if params.TurnID != "" {
				require.Equal(t, turnID, params.TurnID, "事件不能跨回合")
			}
			switch event.Method {
			case "turn/started":
				require.False(t, started, "Turn 只能开始一次")
				require.Equal(t, turnID, params.Turn.ID)
				require.Equal(t, "inProgress", params.Turn.Status)
				started = true
			case "thread/status/changed":
				if params.Status.Type == "active" {
					active = true
				}
				if params.Status.Type == "idle" {
					require.True(t, active)
					idle = true
				}
			case "item/started":
				require.True(t, started)
				require.False(t, itemStarts[params.Item.ID])
				itemStarts[params.Item.ID] = true
			case "item/completed":
				require.True(t, itemStarts[params.Item.ID], "Item 必须先开始再完成")
				require.False(t, itemEnds[params.Item.ID])
				itemEnds[params.Item.ID] = true
				if params.Item.Type == "agentMessage" {
					finalText.WriteString(params.Item.Text)
				}
			case "item/agentMessage/delta":
				require.True(t, started)
				streamedText.WriteString(params.Delta)
			case "thread/tokenUsage/updated":
				require.True(t, started)
				require.Equal(t, 15, params.TokenUsage.Last.TotalTokens)
				require.Equal(t, 10, params.TokenUsage.Last.InputTokens)
				require.Equal(t, 5, params.TokenUsage.Last.OutputTokens)
				require.Equal(t, params.TokenUsage.Last, params.TokenUsage.Total)
				usageSeen = true
			case "turn/completed":
				require.True(t, started)
				require.Equal(t, turnID, params.Turn.ID)
				require.Equal(t, "completed", params.Turn.Status)
				require.Nil(t, params.Turn.Error)
				completed = true
			}
		}
	}
	require.True(t, idle)
	require.True(t, usageSeen)
	require.NotEmpty(t, itemStarts)
	require.Equal(t, itemStarts, itemEnds)
	expected := "CODEX_OK"
	if engine == runtimeidentity.Claude {
		expected = "CLAUDE_OK"
		require.Equal(t, expected, streamedText.String())
	}
	require.Equal(t, expected, finalText.String())
	var history struct {
		Thread struct {
			Turns []struct {
				ID, Status string
				Items      []struct{ Type, Text, ClientID string }
			}
		}
	}
	require.NoError(t, client.Call(ctx, "thread/read", map[string]any{"threadId": threadID, "includeTurns": true}, &history))
	require.Len(t, history.Thread.Turns, 1)
	require.Equal(t, turnID, history.Thread.Turns[0].ID)
	require.Equal(t, "completed", history.Thread.Turns[0].Status)
	var storedText strings.Builder
	for _, item := range history.Thread.Turns[0].Items {
		if item.Type == "agentMessage" {
			storedText.WriteString(item.Text)
		}
		if item.Type == "userMessage" {
			require.Equal(t, "same-message-id", item.ClientID)
		}
	}
	require.Equal(t, finalText.String(), storedText.String(), "历史必须与成功事件一致")
}
