//go:build integration

package bootstrap

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
)

func queueModelPath(engine runtimeidentity.Engine) string {
	if engine == runtimeidentity.Claude {
		return "/v1/messages"
	}
	return "/v1/responses"
}

func queueStateRoot(f controlRuntimeFixture, engine runtimeidentity.Engine) string {
	if engine == runtimeidentity.Claude {
		return f.cfg.ClaudeStateDir()
	}
	return f.cfg.WorkerDataRoot
}

// 标题生成也使用真实 SDK，按其结构化工具区分，不能计入业务模型次数。
func claudeQueueTitleResponse(t *testing.T, w http.ResponseWriter, body []byte) bool {
	t.Helper()
	var payload controlAutomationPayload
	require.NoError(t, json.Unmarshal(body, &payload))
	for _, tool := range payload.Tools {
		if tool.Name == "StructuredOutput" {
			automationToolResponse(w, "queue_title", tool.Name, map[string]any{"title": "队列验收"})
			return true
		}
	}
	return false
}

func claudeQueueLifecycleResponse(t *testing.T, ctx context.Context, w http.ResponseWriter, body []byte,
	entered, release chan struct{}, once *sync.Once, toolsReturned *atomic.Int64,
) {
	t.Helper()
	if claudeQueueTitleResponse(t, w, body) {
		return
	}
	var payload controlAutomationPayload
	require.NoError(t, json.Unmarshal(body, &payload))
	input := automationLatestInput(payload)
	marker := ""
	for _, candidate := range []string{"QUEUE_FIRST", "QUEUE_SECOND", "QUEUE_SECOND_EDITED", "QUEUE_DELETED"} {
		if strings.Contains(input, candidate) {
			marker = candidate
		}
	}
	require.NotEmpty(t, marker)
	require.NotEqual(t, "QUEUE_DELETED", marker)
	callID := "native-" + marker
	if result, found, failed := automationToolResult(payload, callID); found {
		require.False(t, failed)
		require.Contains(t, string(result), "QUEUE_FIRST")
		toolsReturned.Add(1)
		bootstrapModelText(w, true)
		return
	}
	if marker == "QUEUE_FIRST" {
		once.Do(func() { close(entered) })
		select {
		case <-release:
		case <-ctx.Done():
			return
		}
	}
	arguments := map[string]any{"action": "create", "kind": "heartbeat", "name": marker,
		"prompt": marker + "_FOLLOWUP", "schedule": "DTSTART:20300102T000000Z\nRRULE:FREQ=DAILY"}
	if marker == "QUEUE_SECOND_EDITED" {
		arguments = map[string]any{"action": "list"}
	}
	for _, tool := range payload.Tools {
		if strings.Contains(tool.Description, "[tyrs_hand.automation_update]") {
			automationToolResponse(w, callID, tool.Name, arguments)
			return
		}
	}
	t.Error("队列回合未声明真实平台工具")
	bootstrapModelText(w, true)
}

func claudeQueueBindingModel(t *testing.T, calls, denied, accepted *atomic.Int64) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		require.NoError(t, err)
		if claudeQueueTitleResponse(t, w, body) {
			return
		}
		var payload controlAutomationPayload
		require.NoError(t, json.Unmarshal(body, &payload))
		require.LessOrEqual(t, calls.Add(1), int64(5), "不能重放模型或工具")
		marker := ""
		for _, candidate := range []string{"QUEUE_BINDING_WARMUP", "QUEUE_BINDING_OLD", "QUEUE_BINDING_NEW"} {
			if strings.Contains(automationLatestInput(payload), candidate) {
				marker = candidate
			}
		}
		require.NotEmpty(t, marker)
		if marker == "QUEUE_BINDING_WARMUP" {
			bootstrapModelText(w, true)
			return
		}
		if result, found, failed := automationToolResult(payload, marker); found {
			if marker == "QUEUE_BINDING_OLD" {
				require.True(t, failed)
				require.Contains(t, string(result), "Workspace 绑定已失效")
				denied.Add(1)
			} else {
				require.False(t, failed)
				require.Contains(t, string(result), "QUEUE_BINDING_NEW")
				accepted.Add(1)
			}
			bootstrapModelText(w, true)
			return
		}
		for _, tool := range payload.Tools {
			if strings.Contains(tool.Description, "[tyrs_hand.automation_update]") {
				automationToolResponse(w, marker, tool.Name, map[string]any{"action": "create", "kind": "heartbeat",
					"name": marker, "prompt": marker + "_FOLLOWUP", "schedule": "DTSTART:20300102T000000Z\nRRULE:FREQ=DAILY"})
				return
			}
		}
		t.Error("真实 SDK 未声明平台调度工具")
		bootstrapModelText(w, true)
	})
}
