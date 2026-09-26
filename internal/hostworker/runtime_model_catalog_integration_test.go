//go:build integration

package hostworker

import (
	"context"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
)

func TestRuntimeModelCatalogRealSSHBothEngines(t *testing.T) {
	testRuntimeRegistryRealSSH(t, "catalog")
}

func verifyRuntimeModelCatalog(t *testing.T, ctx context.Context, client *codex.SocketClient, engine runtimeidentity.Engine) {
	t.Helper()
	for _, method := range []string{"model/list", "permissionProfile/list"} {
		var full struct {
			Data       []map[string]any `json:"data"`
			NextCursor *string          `json:"nextCursor"`
		}
		require.NoError(t, client.Call(ctx, method, map[string]any{"limit": 1000}, &full))
		require.NotEmpty(t, full.Data, "可选目录不能是假空成功")
		require.Nil(t, full.NextCursor)
		var cursor *string
		var listed []map[string]any
		for page := 0; page <= len(full.Data); page++ {
			var result struct {
				Data       []map[string]any `json:"data"`
				NextCursor *string          `json:"nextCursor"`
			}
			require.NoError(t, client.Call(ctx, method, map[string]any{"limit": 1, "cursor": cursor}, &result))
			require.Len(t, result.Data, 1, "不能忽略分页参数或重复返回空页")
			listed = append(listed, result.Data...)
			cursor = result.NextCursor
			if cursor == nil {
				break
			}
		}
		require.Nil(t, cursor, "分页必须终止")
		require.Equal(t, full.Data, listed, "分页顺序、字段和完整列表必须一致")
		ids := make(map[string]bool)
		for _, item := range listed {
			id, ok := item["id"].(string)
			require.True(t, ok)
			require.NotEmpty(t, id)
			require.False(t, ids[id], "目录不能重复条目")
			ids[id] = true
			if method == "model/list" && engine == runtimeidentity.Claude {
				require.NotContains(t, id, "gpt-", "Claude 入口不能混入 Codex 模型")
				require.Equal(t, id, item["model"])
				require.Equal(t, []any{}, item["serviceTiers"])
				require.Contains(t, item, "defaultServiceTier")
				require.Contains(t, item, "modelSpecialty")
			}
		}
		if method == "permissionProfile/list" {
			for _, id := range []string{":read-only", ":workspace", ":danger-full-access"} {
				require.True(t, ids[id], "必须向客户端提供权限档位 %s", id)
			}
		}
	}
	var collaboration struct {
		Data []struct {
			Mode string `json:"mode"`
		} `json:"data"`
	}
	require.NoError(t, client.Call(ctx, "collaborationMode/list", map[string]any{}, &collaboration))
	modes := make([]string, 0, len(collaboration.Data))
	for _, entry := range collaboration.Data {
		modes = append(modes, entry.Mode)
	}
	require.Contains(t, modes, "plan")
	require.Contains(t, modes, "default")
	var capabilities map[string]any
	require.NoError(t, client.Call(ctx, "modelProvider/capabilities/read", map[string]any{}, &capabilities))
	for _, name := range []string{"namespaceTools", "imageGeneration", "webSearch"} {
		_, ok := capabilities[name].(bool)
		require.True(t, ok, "能力必须是明确布尔值：%s", name)
	}
}
