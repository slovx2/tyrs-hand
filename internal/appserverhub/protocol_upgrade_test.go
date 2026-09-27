package appserverhub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/stretchr/testify/require"
)

// 升级新增接口必须显式分类，不能借未知方法透传掩盖遗漏。
func TestProtocolUpgradeClassifiesNewRequests(t *testing.T) {
	previous := protocolMethods(t, "0.147.0", "ClientRequest")
	current := protocolMethods(t, codex.RequiredVersion, "ClientRequest")
	added := 0
	for method := range current {
		if previous[method] {
			continue
		}
		added++
		require.NotEqual(t, methodUnknown, classifyMethod(method), method)
	}
	require.Equal(t, 35, added)
	require.False(t, current["thread/rollback"])
	require.Equal(t, methodControlled, classifyMethod("thread/rollback"))
	require.Equal(t, methodControlled, classifyMethod("thread/revert"))
	require.Equal(t, methodControlled, classifyMethod("thread/queue/start"))
}

func TestProtocolUpgradeRegistersNewRequestsAndNotifications(t *testing.T) {
	data, err := os.ReadFile("../../protocol/runtime-matrix.json")
	require.NoError(t, err)
	var matrix struct {
		ProtocolVersion string `json:"protocolVersion"`
		ReleaseReady    bool   `json:"releaseReady"`
		Methods         []struct {
			Method  string            `json:"method"`
			Engines map[string]string `json:"engines"`
			Schema  json.RawMessage   `json:"schema"`
		} `json:"methods"`
	}
	require.NoError(t, json.Unmarshal(data, &matrix))
	require.Equal(t, codex.RequiredVersion, matrix.ProtocolVersion)
	registered := make(map[string]bool)
	for _, entry := range matrix.Methods {
		require.False(t, registered[entry.Method], "重复方法: %s", entry.Method)
		registered[entry.Method] = len(entry.Schema) > 0 && string(entry.Schema) != "null"
	}
	for _, kind := range []string{"ClientRequest", "ServerNotification"} {
		previous := protocolMethods(t, "0.147.0", kind)
		for method := range protocolMethods(t, codex.RequiredVersion, kind) {
			if !previous[method] {
				require.True(t, registered[method], "缺少新接口或 schema: %s", method)
			}
		}
	}
}

func protocolMethods(t *testing.T, version, kind string) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("../../protocol/codex-app-server", version, "json-schema", kind+".json"))
	require.NoError(t, err)
	var schema struct {
		OneOf []struct {
			Properties struct {
				Method struct {
					Enum []string `json:"enum"`
				} `json:"method"`
			} `json:"properties"`
		} `json:"oneOf"`
	}
	require.NoError(t, json.Unmarshal(data, &schema))
	result := make(map[string]bool)
	for _, variant := range schema.OneOf {
		for _, method := range variant.Properties.Method.Enum {
			result[method] = true
		}
	}
	return result
}
