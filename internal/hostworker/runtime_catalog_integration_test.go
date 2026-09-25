//go:build integration

package hostworker

import (
	"context"
	"fmt"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/stretchr/testify/require"
)

func verifyClaudeCatalog(t *testing.T, ctx context.Context, client *codex.SocketClient, cwd string) {
	t.Helper()
	ids := make([]string, 0, 8)
	for index := 0; index < 8; index++ {
		var result struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
		}
		require.NoError(t, client.Call(ctx, "thread/start", map[string]any{"cwd": cwd}, &result))
		ids = append(ids, result.Thread.ID)
		var ignored any
		require.NoError(t, client.Call(ctx, "thread/name/set", map[string]any{
			"threadId": result.Thread.ID, "name": fmt.Sprintf("catalog-%d", index),
		}, &ignored))
	}
	for _, direction := range []string{"asc", "desc"} {
		var cursor *string
		var listed []string
		for page := 0; page < 10; page++ {
			var result struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
				NextCursor *string `json:"nextCursor"`
			}
			require.NoError(t, client.Call(ctx, "thread/list", map[string]any{
				"cwd": cwd, "sortDirection": direction, "cursor": cursor, "limit": 2,
			}, &result))
			for _, thread := range result.Data {
				listed = append(listed, thread.ID)
			}
			cursor = result.NextCursor
			if cursor == nil {
				break
			}
		}
		require.Nil(t, cursor, "分页必须终止")
		require.ElementsMatch(t, ids, listed, "真实 SSH 列表不能漏项或重复")
	}
	var cursor *string
	var loaded []string
	for page := 0; page < 10; page++ {
		var result struct {
			Data       []string `json:"data"`
			NextCursor *string  `json:"nextCursor"`
		}
		require.NoError(t, client.Call(ctx, "thread/loaded/list", map[string]any{"cursor": cursor, "limit": 2}, &result))
		loaded = append(loaded, result.Data...)
		cursor = result.NextCursor
		if cursor == nil {
			break
		}
	}
	require.Nil(t, cursor)
	require.ElementsMatch(t, ids, loaded)
}
