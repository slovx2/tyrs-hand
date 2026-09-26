//go:build integration

package hostworker

import (
	"context"
	"testing"

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
	return goal
}

func verifyCodexPausedGoalAfterRestart(t *testing.T, ctx context.Context, client *codex.SocketClient, expected nativePausedGoal) {
	t.Helper()
	result := nativeMetadataCall[nativeGoalResponse](t, ctx, client, "thread/goal/get", map[string]any{"threadId": expected.ThreadID})
	require.NotNil(t, result.Goal)
	require.Equal(t, expected, *result.Goal, "重启不能丢失目标或将暂停状态改为继续执行")
	cleared := nativeMetadataCall[struct{ Cleared bool }](t, ctx, client, "thread/goal/clear", map[string]any{"threadId": expected.ThreadID})
	require.True(t, cleared.Cleared)
	require.Nil(t, nativeMetadataCall[nativeGoalResponse](t, ctx, client, "thread/goal/get", map[string]any{"threadId": expected.ThreadID}).Goal)
}
