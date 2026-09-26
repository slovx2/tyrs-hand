//go:build integration

package hostworker

import (
	"context"
	"maps"
	"slices"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestRuntimeCodexItemsRealSSH(t *testing.T) {
	testRuntimeRegistryRealSSH(t, "codex-items")
}

type nativeItemsPage struct {
	Data                        []runtimeHistoryItem
	NextCursor, BackwardsCursor *string
}

func nativeItemsExpected(t *testing.T, ctx context.Context, client *codex.SocketClient, threadID string) []runtimeHistoryItem {
	t.Helper()
	source := nativeMetadataCall[struct {
		Data       []runtimeHistoryTurn
		NextCursor *string
	}](t, ctx, client, "thread/turns/list", map[string]any{"threadId": threadID, "limit": 100, "sortDirection": "asc", "itemsView": "full"})
	require.Nil(t, source.NextCursor)
	expected := []runtimeHistoryItem{}
	for _, turn := range source.Data {
		require.Equal(t, "full", turn.ItemsView)
		for _, item := range turn.Items {
			expected = append(expected, runtimeHistoryItem{TurnID: turn.ID, Item: item})
		}
	}
	return expected
}

func verifyNativeItemsPages(t *testing.T, ctx context.Context, client *codex.SocketClient, threadID string, expected []runtimeHistoryItem) {
	t.Helper()
	scopes := []string{"", expected[0].TurnID, expected[len(expected)-1].TurnID}
	for _, turnID := range scopes {
		for _, direction := range []string{"asc", "desc"} {
			wanted := []runtimeHistoryItem{}
			for _, item := range expected {
				if turnID == "" || item.TurnID == turnID {
					wanted = append(wanted, item)
				}
			}
			if direction == "desc" {
				slices.Reverse(wanted)
			}
			params := map[string]any{"threadId": threadID, "limit": 1, "sortDirection": direction}
			if turnID != "" {
				params["turnId"] = turnID
			}
			actual := []runtimeHistoryItem{}
			cursors := map[string]bool{}
			for pageNumber := 0; pageNumber <= len(wanted); pageNumber++ {
				page := nativeMetadataCall[nativeItemsPage](t, ctx, client, "thread/items/list", params)
				require.Len(t, page.Data, 1)
				require.NotNil(t, page.BackwardsCursor)
				actual = append(actual, page.Data...)
				if page.NextCursor == nil {
					break
				}
				require.False(t, cursors[*page.NextCursor], "条目分页游标不能循环")
				cursors[*page.NextCursor] = true
				params["cursor"] = *page.NextCursor
			}
			require.Equal(t, wanted, actual, "分页必须逐条保留原生完整历史及所属 Turn")
		}
	}
	unpaged := nativeMetadataCall[nativeItemsPage](t, ctx, client, "thread/items/list", map[string]any{"threadId": threadID})
	require.Equal(t, expected, unpaged.Data, "默认方向应为升序")
	require.Nil(t, unpaged.NextCursor)
}

func requireNativeItemsInvalid(t *testing.T, ctx context.Context, client *codex.SocketClient, params map[string]any) {
	t.Helper()
	err := client.Call(ctx, "thread/items/list", params, nil)
	var rejected *codex.RPCError
	require.ErrorAs(t, err, &rejected)
	require.Equal(t, -32602, rejected.Code)
}

// HISTORY-005：真实两会话三 Turn；跨 Thread/Turn/方向的游标拒绝、重启及回退失效。
func verifyCodexNativeItems(t *testing.T, ctx context.Context, client *codex.SocketClient, root string, registry *RuntimeRegistry, connection *ssh.Client) {
	t.Helper()
	otherGeneration := registry.entries[runtimeidentity.Claude].Runtime.Generation()
	start := func() sessionThread {
		return readSessionThread(t, ctx, client, "thread/start", map[string]any{"cwd": root, "approvalPolicy": "never", "sandbox": "danger-full-access"})
	}
	emptyThread := start()
	var unmaterializedNative, unmaterializedItems *codex.RPCError
	emptyParams := map[string]any{"threadId": emptyThread.ID}
	require.ErrorAs(t, client.Call(ctx, "thread/turns/list", emptyParams, nil), &unmaterializedNative)
	require.ErrorAs(t, client.Call(ctx, "thread/items/list", emptyParams, nil), &unmaterializedItems)
	require.Equal(t, unmaterializedNative.Code, unmaterializedItems.Code, "尚未实体化的原生会话必须保留错误码")
	require.Equal(t, unmaterializedNative.Message, unmaterializedItems.Message)
	thread := start()
	firstTurn := runNativeMetadataTurn(t, ctx, client, thread.ID, "ITEMS_REAL_FIRST")
	secondTurn := runNativeMetadataTurn(t, ctx, client, thread.ID, "ITEMS_REAL_SECOND")
	other := start()
	runNativeMetadataTurn(t, ctx, client, other.ID, "ITEMS_OTHER_THREAD")
	expected := nativeItemsExpected(t, ctx, client, thread.ID)
	require.Len(t, expected, 4)
	require.Equal(t, firstTurn, expected[0].TurnID)
	require.Equal(t, secondTurn, expected[2].TurnID)
	otherExpected := nativeItemsExpected(t, ctx, client, other.ID)
	require.Len(t, otherExpected, 2)
	verifyNativeItemsPages(t, ctx, client, thread.ID, expected)
	verifyNativeItemsPages(t, ctx, client, other.ID, otherExpected)
	first := nativeMetadataCall[nativeItemsPage](t, ctx, client, "thread/items/list", map[string]any{"threadId": thread.ID, "limit": 2})
	require.Equal(t, expected[:2], first.Data)
	require.NotNil(t, first.NextCursor)
	second := nativeMetadataCall[nativeItemsPage](t, ctx, client, "thread/items/list", map[string]any{"threadId": thread.ID, "limit": 2, "cursor": *first.NextCursor})
	require.Equal(t, expected[2:], second.Data)
	require.Nil(t, second.NextCursor)
	require.NotNil(t, second.BackwardsCursor)
	backwards := nativeMetadataCall[nativeItemsPage](t, ctx, client, "thread/items/list", map[string]any{"threadId": thread.ID, "limit": 2, "cursor": *second.BackwardsCursor, "sortDirection": "desc"})
	require.Equal(t, []runtimeHistoryItem{expected[2], expected[1]}, backwards.Data, "反向分页包含锚点以接收更新")
	for _, changes := range []map[string]any{{"threadId": other.ID}, {"turnId": firstTurn}, {"turnId": secondTurn}, {"sortDirection": "desc"}, {"cursor": "invalid"}, {"cursor": ""}, {"limit": 0}, {"limit": 1001}, {"threadId": ""}, {"turnId": ""}} {
		params := map[string]any{"threadId": thread.ID, "cursor": *first.NextCursor}
		maps.Copy(params, changes)
		requireNativeItemsInvalid(t, ctx, client, params)
	}
	requireNativeItemsInvalid(t, ctx, client, map[string]any{"threadId": thread.ID, "cursor": *second.BackwardsCursor, "sortDirection": "asc"})
	unknown := map[string]any{"threadId": "00000000-0000-4000-8000-000000000001"}
	var nativeError, itemError *codex.RPCError
	require.ErrorAs(t, client.Call(ctx, "thread/turns/list", unknown, nil), &nativeError)
	require.ErrorAs(t, client.Call(ctx, "thread/items/list", unknown, nil), &itemError)
	require.Equal(t, nativeError.Code, itemError.Code, "未知会话必须保留原生错误码")
	require.Equal(t, nativeError.Message, itemError.Message)
	empty := nativeMetadataCall[nativeItemsPage](t, ctx, client, "thread/items/list", map[string]any{"threadId": thread.ID, "turnId": otherExpected[0].TurnID})
	require.Empty(t, empty.Data, "其他会话的 Turn 不能泄漏历史")
	require.Nil(t, empty.NextCursor)
	require.Nil(t, empty.BackwardsCursor)
	stale := nativeMetadataCall[nativeItemsPage](t, ctx, client, "thread/items/list", map[string]any{"threadId": thread.ID, "turnId": secondTurn, "limit": 1})
	require.NotNil(t, stale.NextCursor)
	require.NoError(t, registry.Restart(runtimeidentity.Codex))
	client = connectRuntimeSSH(t, ctx, connection, runtimeidentity.Codex)
	verifyNativeItemsPages(t, ctx, client, thread.ID, expected)
	require.Equal(t, expected[2:], nativeMetadataCall[nativeItemsPage](t, ctx, client, "thread/items/list", map[string]any{"threadId": thread.ID, "cursor": *first.NextCursor}).Data, "原生持久历史支持重启前的游标")
	readSessionThread(t, ctx, client, "thread/resume", map[string]any{"threadId": thread.ID})
	rolled := readSessionThread(t, ctx, client, "thread/rollback", map[string]any{"threadId": thread.ID, "numTurns": 1})
	require.Len(t, rolled.Turns, 1)
	requireNativeItemsInvalid(t, ctx, client, map[string]any{"threadId": thread.ID, "turnId": secondTurn, "cursor": *stale.NextCursor})
	require.Equal(t, expected[:2], nativeMetadataCall[nativeItemsPage](t, ctx, client, "thread/items/list", map[string]any{"threadId": thread.ID}).Data)
	require.Equal(t, otherExpected, nativeMetadataCall[nativeItemsPage](t, ctx, client, "thread/items/list", map[string]any{"threadId": other.ID}).Data)
	require.Equal(t, otherGeneration, registry.entries[runtimeidentity.Claude].Runtime.Generation())
}
