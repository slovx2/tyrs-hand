//go:build integration

package hostworker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/stretchr/testify/require"
)

type nativePausedGoal struct {
	ThreadID, Objective, Status                                    string
	TokenBudget, TokensUsed, TimeUsedSeconds, CreatedAt, UpdatedAt int64
}

type nativeGoalResponse struct{ Goal *nativePausedGoal }

// GOAL-004：暂停目标的原生持久 CRUD，不替代 GOAL-002 自动续跑和预算计量。
func createCodexPausedGoal(t *testing.T, ctx context.Context, client *codex.SocketClient, threadID string) nativePausedGoal {
	t.Helper()
	events := client.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	defer events.Close()
	absent := nativeMetadataCall[nativeGoalResponse](t, ctx, client, "thread/goal/get", map[string]any{"threadId": threadID})
	require.Nil(t, absent.Goal)
	result := nativeMetadataCall[nativeGoalResponse](t, ctx, client, "thread/goal/set", map[string]any{"threadId": threadID, "objective": "Persist paused SSH goal", "status": "paused", "tokenBudget": 50000})
	require.NotNil(t, result.Goal)
	goal := *result.Goal
	require.Equal(t, threadID, goal.ThreadID)
	require.Equal(t, "Persist paused SSH goal", goal.Objective)
	require.Equal(t, "paused", goal.Status)
	require.Equal(t, int64(50000), goal.TokenBudget)
	require.Zero(t, goal.TokensUsed)
	require.Zero(t, goal.TimeUsedSeconds)
	require.Positive(t, goal.CreatedAt)
	require.GreaterOrEqual(t, goal.UpdatedAt, goal.CreatedAt)
	require.Equal(t, result, nativeMetadataCall[nativeGoalResponse](t, ctx, client, "thread/goal/get", map[string]any{"threadId": threadID}))
	updated := waitNativeMetadataNotification(t, ctx, events, "thread/goal/updated", threadID)
	var notification struct {
		Goal   nativePausedGoal
		TurnID *string
	}
	require.NoError(t, json.Unmarshal(updated, &notification))
	require.Equal(t, goal, notification.Goal, "通知必须完整反映原生持久化目标")
	require.Nil(t, notification.TurnID, "暂停目标 CRUD 不能关联或自动创建回合")
	requireNativeMetadataQuiet(t, events, "thread/goal/updated")
	return goal
}

func verifyCodexPausedGoalAfterRestart(t *testing.T, ctx context.Context, client *codex.SocketClient, expected nativePausedGoal) {
	t.Helper()
	events := client.Subscribe(codex.ThreadFilter{ThreadID: expected.ThreadID})
	defer events.Close()
	result := nativeMetadataCall[nativeGoalResponse](t, ctx, client, "thread/goal/get", map[string]any{"threadId": expected.ThreadID})
	require.NotNil(t, result.Goal)
	require.Equal(t, expected, *result.Goal, "重启不能丢失目标或将暂停状态改为继续执行")
	cleared := nativeMetadataCall[struct{ Cleared bool }](t, ctx, client, "thread/goal/clear", map[string]any{"threadId": expected.ThreadID})
	require.True(t, cleared.Cleared)
	clearedEvent := waitNativeMetadataNotification(t, ctx, events, "thread/goal/cleared", expected.ThreadID)
	require.JSONEq(t, fmtNativeThreadID(expected.ThreadID), string(clearedEvent))
	require.Nil(t, nativeMetadataCall[nativeGoalResponse](t, ctx, client, "thread/goal/get", map[string]any{"threadId": expected.ThreadID}).Goal)
	require.False(t, nativeMetadataCall[struct{ Cleared bool }](t, ctx, client, "thread/goal/clear", map[string]any{"threadId": expected.ThreadID}).Cleared)
	requireNativeMetadataQuiet(t, events, "thread/goal/cleared")
}

func fmtNativeThreadID(threadID string) string {
	value, _ := json.Marshal(map[string]string{"threadId": threadID})
	return string(value)
}

func waitNativeMetadataNotification(t *testing.T, ctx context.Context, events *codex.EventSubscription, method, threadID string) json.RawMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("真实 SSH 未收到 %s", method)
		case event, ok := <-events.Events():
			require.True(t, ok, "目标通知前连接不能关闭")
			require.NotEqual(t, "turn/started", event.Method, "元数据操作不能自动创建模型回合")
			if event.Method != method {
				continue
			}
			var params struct{ ThreadID string }
			require.NoError(t, json.Unmarshal(event.Params, &params))
			require.Equal(t, threadID, params.ThreadID)
			return event.Params
		}
	}
}

func requireNativeMetadataQuiet(t *testing.T, events *codex.EventSubscription, method string) {
	t.Helper()
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			return
		case event, ok := <-events.Events():
			require.True(t, ok)
			require.NotContains(t, []string{method, "turn/started"}, event.Method,
				"元数据通知必须恰好一次，空操作不能重复发出成功通知或启动模型")
		}
	}
}
