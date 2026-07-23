//go:build integration

package integration

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codexrelay"
	"github.com/stretchr/testify/require"
)

func TestRealCodexRelayClassifiesEveryClientRequestMethod(t *testing.T) {
	output := filepath.Join(t.TempDir(), "schema")
	command := exec.Command(fixedCodexBinary(t), "app-server", "generate-json-schema", "--out", output)
	data, err := command.CombinedOutput()
	require.NoError(t, err, string(data))
	raw, err := os.ReadFile(filepath.Join(output, "ClientRequest.json"))
	require.NoError(t, err)
	var schema any
	require.NoError(t, json.Unmarshal(raw, &schema))
	methods := make(map[string]bool)
	collectSchemaMethods(schema, methods)
	require.NotEmpty(t, methods)

	classified := codexrelay.ClassifiedMethods()
	// Desktop 0.145.0 会调用该方法，app-server 也能处理并广播结果，
	// 但 generate-json-schema 尚未把它写入 ClientRequest。
	unschematizedDesktopMethods := map[string]bool{
		"thread/settings/update": true,
	}
	missing := make([]string, 0)
	extra := make([]string, 0)
	for method := range methods {
		if _, ok := classified[method]; !ok {
			missing = append(missing, method)
		}
	}
	for method := range classified {
		if !methods[method] && !unschematizedDesktopMethods[method] {
			extra = append(extra, method)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	require.Empty(t, missing, "真实 Codex 出现未分类方法")
	require.Empty(t, extra, "分类表包含固定版本不存在的方法")
}

func collectSchemaMethods(value any, methods map[string]bool) {
	switch item := value.(type) {
	case map[string]any:
		if method, ok := item["method"].(map[string]any); ok {
			if values, ok := method["enum"].([]any); ok {
				for _, candidate := range values {
					if text, ok := candidate.(string); ok {
						methods[text] = true
					}
				}
			}
		}
		for _, child := range item {
			collectSchemaMethods(child, methods)
		}
	case []any:
		for _, child := range item {
			collectSchemaMethods(child, methods)
		}
	}
}
