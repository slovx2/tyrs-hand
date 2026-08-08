//go:build integration

package protocol

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/stretchr/testify/require"
)

func fixedCodexBinary(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("TYRS_HAND_TEST_CODEX_BIN")
	if bin == "" {
		bin = "codex"
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		if os.Getenv("CI") == "true" {
			t.Fatalf("CI 缺少固定 Codex: %v", err)
		}
		t.Skip("本机没有安装固定版本 Codex")
	}
	require.NoError(t, codex.ValidateVersion(context.Background(), path))
	return path
}

func temporaryDir(t *testing.T, prefix string) string {
	t.Helper()
	root, err := os.MkdirTemp("", prefix)
	require.NoError(t, err)
	t.Cleanup(func() {
		var removeErr error
		for attempt := 0; attempt < 10; attempt++ {
			removeErr = os.RemoveAll(root)
			if removeErr == nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		require.NoError(t, removeErr)
	})
	return root
}

func sse(events ...map[string]any) string {
	var result strings.Builder
	for _, event := range events {
		data, _ := json.Marshal(event)
		_, _ = fmt.Fprintf(&result, "event: %s\ndata: %s\n\n", event["type"], data)
	}
	return result.String()
}

func completedResponse(id string) map[string]any {
	return map[string]any{
		"type": "response.completed",
		"response": map[string]any{"id": id, "usage": map[string]any{
			"input_tokens": 0, "input_tokens_details": nil, "output_tokens": 0,
			"output_tokens_details": nil, "total_tokens": 0,
		}},
	}
}
