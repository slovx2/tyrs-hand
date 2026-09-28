//go:build integration

package hostworker

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestRuntimeCodexTimelineRealSSH(t *testing.T) {
	testRuntimeRegistryRealSSH(t, "codex-timeline")
}

// HISTORY-006：普通原生回合的时间线、反向翻页、线程隔离与重启持久。
// 业务成功不能替代固定官方 schema；不将该用例登记为实时语音验收。
func verifyCodexNativeTimeline(t *testing.T, ctx context.Context, client *codex.SocketClient, root string, registry *RuntimeRegistry, connection *ssh.Client) {
	t.Helper()
	histories := map[string][]map[string]any{}
	for _, text := range []string{"TIMELINE_FIRST_HISTORY", "TIMELINE_OTHER_HISTORY"} {
		thread := readSessionThread(t, ctx, client, "thread/start", map[string]any{
			"cwd": filepath.Join(root, "project"), "sandbox": "danger-full-access", "approvalPolicy": "never"})
		runNativeMetadataTurn(t, ctx, client, thread.ID, text)
		histories[thread.ID] = nativeTimeline(t, ctx, client, thread.ID)
	}
	otherGeneration := registry.entries[runtimeidentity.Claude].Runtime.Generation()
	require.NoError(t, registry.Restart(runtimeidentity.Codex))
	client = connectRuntimeSSH(t, ctx, connection, runtimeidentity.Codex)
	require.Equal(t, otherGeneration, registry.entries[runtimeidentity.Claude].Runtime.Generation())
	for threadID, before := range histories {
		require.Equal(t, before, nativeTimeline(t, ctx, client, threadID), "重启不能改变时间线的原生边界、条目及其内容")
	}
}

func nativeTimeline(t *testing.T, ctx context.Context, client *codex.SocketClient, threadID string) []map[string]any {
	t.Helper()
	var cursor *string
	entries := []map[string]any{}
	seen := map[float64]bool{}
	for page := 0; page < 20; page++ {
		result := nativeMetadataCall[struct {
			Data                             []map[string]any
			NextCursor                       *string
			ActiveRealtimeSessionAtPageStart *string
		}](t, ctx, client, "thread/timeline/list", map[string]any{
			"threadId": threadID, "limit": 1, "cursor": cursor})
		require.Len(t, result.Data, 1, "已完成回合的时间线分页必须持续推进")
		require.Nil(t, result.ActiveRealtimeSessionAtPageStart, "普通回合不应生成实时语音会话")
		entry := result.Data[0]
		require.IsType(t, float64(0), entry["position"])
		position := entry["position"].(float64)
		require.False(t, seen[position], "时间线分页不能重复或循环")
		if len(entries) > 0 {
			require.Less(t, position, entries[0]["position"].(float64), "nextCursor 必须向更早的原生位置推进")
		}
		seen[position] = true
		// 原生时间线从最近一页向前翻页；页内仍为 rollout 升序。
		entries = append(result.Data, entries...)
		cursor = result.NextCursor
		if cursor == nil {
			break
		}
	}
	require.Nil(t, cursor, "时间线分页必须收敛")
	full := nativeMetadataCall[struct {
		Data       []map[string]any
		NextCursor *string
	}](t, ctx, client, "thread/timeline/list", map[string]any{"threadId": threadID, "limit": 1000})
	require.Nil(t, full.NextCursor)
	require.Equal(t, full.Data, entries, "逐页拼接必须等于原生完整时间线，不能漏项或改写内容")
	history := readSessionThread(t, ctx, client, "thread/read", map[string]any{"threadId": threadID, "includeTurns": true})
	require.Len(t, history.Turns, 1)
	counts := map[string]int{}
	for _, entry := range entries {
		require.Equal(t, history.Turns[0].ID, entry["turnId"], "时间线不能混入其他会话回合")
		kind, ok := entry["type"].(string)
		require.True(t, ok)
		require.Contains(t, []string{"item", "turnStarted", "turnCompleted"}, kind)
		counts[kind]++
		if kind == "turnCompleted" {
			require.Equal(t, "completed", entry["status"])
			require.Nil(t, entry["error"])
		}
	}
	require.Equal(t, 1, counts["turnStarted"])
	require.Equal(t, 1, counts["turnCompleted"])
	require.GreaterOrEqual(t, counts["item"], 2, "原生用户消息与助手回答必须进入时间线")
	return entries
}
