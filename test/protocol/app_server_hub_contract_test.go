//go:build integration

package protocol

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/appserverhub"
	"github.com/stretchr/testify/require"
)

func TestRealCodexHubClassifiesEveryClientRequestMethod(t *testing.T) {
	// 稳定方法全部必须分类；已接入的实验方法必须在同一固定 CLI 中真实存在。
	required := generatedRequestMethods(t, false)
	known := generatedRequestMethods(t, true)
	collectExtensionMethods(t, required)
	collectExtensionMethods(t, known)

	classified := appserverhub.ClassifiedMethods()
	missing := make([]string, 0)
	extra := make([]string, 0)
	for method := range required {
		if _, ok := classified[method]; !ok {
			missing = append(missing, method)
		}
	}
	for method := range classified {
		if !known[method] {
			extra = append(extra, method)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	require.Empty(t, missing, "真实 Codex 出现未分类方法")
	require.Empty(t, extra, "分类表包含未在固定版本或扩展 schema 登记的方法")
}

func generatedRequestMethods(t *testing.T, experimental bool) map[string]bool {
	t.Helper()
	output := filepath.Join(t.TempDir(), "schema")
	arguments := []string{"app-server", "generate-json-schema", "--out", output}
	if experimental {
		arguments = append(arguments, "--experimental")
	}
	data, err := exec.Command(fixedCodexBinary(t), arguments...).CombinedOutput()
	require.NoError(t, err, string(data))
	raw, err := os.ReadFile(filepath.Join(output, "ClientRequest.json"))
	require.NoError(t, err)
	var schema any
	require.NoError(t, json.Unmarshal(raw, &schema))
	methods := make(map[string]bool)
	collectSchemaMethods(schema, methods)
	require.NotEmpty(t, methods)
	return methods
}

// 扩展与 wire 校验共用登记文件，不能靠跳过未知方法放宽分类门禁。
func collectExtensionMethods(t *testing.T, methods map[string]bool) {
	t.Helper()
	directory := filepath.Join("..", "..", "protocol", "extensions")
	files, err := os.ReadDir(directory)
	require.NoError(t, err)
	for _, file := range files {
		if file.IsDir() || filepath.Ext(file.Name()) != ".json" {
			continue
		}
		raw, readErr := os.ReadFile(filepath.Join(directory, file.Name()))
		require.NoError(t, readErr)
		var extension struct {
			Method, Kind     string
			Params, Response map[string]any
		}
		require.NoError(t, json.Unmarshal(raw, &extension), file.Name())
		require.NotEmpty(t, extension.Method, file.Name())
		require.Contains(t, []string{"ClientRequest", "ServerRequest", "ClientNotification", "ServerNotification"}, extension.Kind, file.Name())
		if extension.Kind != "ClientRequest" {
			continue
		}
		require.NotEmpty(t, extension.Params, "扩展请求必须登记参数 schema: %s", file.Name())
		require.NotEmpty(t, extension.Response, "扩展请求必须登记响应 schema: %s", file.Name())
		require.False(t, methods[extension.Method], "扩展不能覆盖已有 schema: %s", extension.Method)
		methods[extension.Method] = true
	}
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
