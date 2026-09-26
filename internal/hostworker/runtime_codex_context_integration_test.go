//go:build integration

package hostworker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestRuntimeCodexContextRealSSH(t *testing.T) {
	testRuntimeRegistryRealSSH(t, "codex-context")
}

type runtimeCodexContextFixture struct{ calls atomic.Int64 }

const (
	codexContextBase     = "CONTEXT_007_NATIVE_BASE"
	codexContextInjected = "CONTEXT_007_INJECTED_USER_CONTENT"
	codexContextSummary  = "CONTEXT_007_NATIVE_COMPACT_SUMMARY"
)

func (f *runtimeCodexContextFixture) model(t *testing.T, w http.ResponseWriter, request *http.Request, body []byte) {
	t.Helper()
	step := f.calls.Add(1)
	var input struct{ Input json.RawMessage }
	require.NoError(t, json.Unmarshal(body, &input))
	text := string(input.Input)
	t.Logf("CONTEXT-007 model step=%d path=%s base=%t injected=%t summary=%t", step, request.URL.Path,
		strings.Contains(text, codexContextBase), strings.Contains(text, codexContextInjected), strings.Contains(text, codexContextSummary))
	require.Equal(t, "/v1/responses", request.URL.Path, "本用例必须记录真实原生压缩路径，不能伪造其他端点成功")
	answer := "CONTEXT_007_BASE_REPLY"
	switch step {
	case 1:
		require.Contains(t, text, codexContextBase)
		require.NotContains(t, text, codexContextInjected)
	case 2:
		require.Contains(t, text, codexContextBase)
		require.Equal(t, 1, strings.Count(text, codexContextInjected), "真实注入在重启后恰好进入模型一次")
		require.Contains(t, text, "CONTEXT_007_AFTER_INJECT")
		answer = "CONTEXT_007_INJECT_REPLY"
	case 3:
		require.Contains(t, text, codexContextBase)
		require.Contains(t, text, codexContextInjected)
		require.Contains(t, text, "CONTEXT_007_INJECT_REPLY")
		require.Contains(t, strings.ToLower(text), "summar", "第三次必须是真实CLI压缩提示词")
		answer = codexContextSummary + ": Preserve the injected instruction and continue the fixture task."
	case 4:
		require.Equal(t, 1, strings.Count(text, codexContextSummary), "重启后必须使用原生压缩结果")
		require.NotContains(t, text, "CONTEXT_007_BASE_REPLY", "被摘要替换的旧助手正文不能重新入模")
		require.Contains(t, text, "CONTEXT_007_AFTER_COMPACT")
		answer = "CONTEXT_007_FINAL_REPLY"
	default:
		t.Errorf("管理、注入或重启触发额外模型请求: %d", step)
		http.Error(w, "unexpected context model call", http.StatusBadRequest)
		return
	}
	runtimeTextModel(w, request, answer, fmt.Sprintf("codex-context-%d", step))
}

// CONTEXT-007：真实 Codex SSH 注入、原生压缩与两次重启，历史全部由 CLI 生成。
func verifyRuntimeCodexContext(t *testing.T, ctx context.Context, registry *RuntimeRegistry,
	connection *ssh.Client, fixture *runtimeCodexContextFixture, root string,
) {
	t.Helper()
	otherGeneration := registry.entries[runtimeidentity.Claude].Runtime.Generation()
	connect := func() *codex.SocketClient {
		client, _ := connectRuntimeSSHWithTrace(t, ctx, connection, runtimeidentity.Codex, codex.SocketClientOptions{})
		return client
	}
	client := connect()
	thread := readSessionThread(t, ctx, client, "thread/start", map[string]any{
		"cwd": root, "approvalPolicy": "never", "sandbox": "danger-full-access",
	})
	first := runNativeMetadataTurn(t, ctx, client, thread.ID, codexContextBase)
	require.Equal(t, int64(1), fixture.calls.Load())
	before := readSessionThread(t, ctx, client, "thread/read", map[string]any{"threadId": thread.ID, "includeTurns": true})
	require.Len(t, before.Turns, 1)
	require.NoError(t, client.Call(ctx, "thread/inject_items", map[string]any{
		"threadId": thread.ID, "items": []any{map[string]any{
			"type": "message", "role": "user",
			"content": []map[string]string{{"type": "input_text", "text": codexContextInjected}},
		}},
	}, nil))
	require.Equal(t, int64(1), fixture.calls.Load(), "注入协议必须零模型")
	after := readSessionThread(t, ctx, client, "thread/read", map[string]any{"threadId": thread.ID, "includeTurns": true})
	require.Len(t, after.Turns, 1, "注入不能预造助手回合")
	require.Equal(t, first, after.Turns[0].ID)
	require.NoError(t, registry.Restart(runtimeidentity.Codex))
	client = connect()
	readSessionThread(t, ctx, client, "thread/resume", map[string]any{"threadId": thread.ID})
	require.Equal(t, int64(1), fixture.calls.Load(), "恢复注入不能自动请求模型")
	second := runNativeMetadataTurn(t, ctx, client, thread.ID, "CONTEXT_007_AFTER_INJECT")
	require.NotEqual(t, first, second)
	require.Equal(t, int64(2), fixture.calls.Load())
	compactTurn := runCodexNativeCompaction(t, ctx, client, thread.ID)
	require.Equal(t, int64(3), fixture.calls.Load(), "手动压缩只能新增一次原生摘要请求")
	compacted := nativeMetadataCall[codexContextHistory](t, ctx, client, "thread/read", map[string]any{"threadId": thread.ID, "includeTurns": true})
	requireNativeCompactionHistory(t, compacted, compactTurn)
	require.NoError(t, registry.Restart(runtimeidentity.Codex))
	client = connect()
	restored := nativeMetadataCall[codexContextHistory](t, ctx, client, "thread/resume", map[string]any{"threadId": thread.ID})
	requireNativeCompactionHistory(t, restored, compactTurn)
	require.Equal(t, int64(3), fixture.calls.Load(), "恢复压缩历史必须零模型")
	last := runNativeMetadataTurn(t, ctx, client, thread.ID, "CONTEXT_007_AFTER_COMPACT")
	require.NotEqual(t, compactTurn, last)
	require.Equal(t, int64(4), fixture.calls.Load())
	require.Equal(t, otherGeneration, registry.entries[runtimeidentity.Claude].Runtime.Generation())
}

type codexContextHistory struct {
	Thread struct {
		Turns []struct {
			ID, Status string
			Items      []json.RawMessage
		}
	}
}

func requireNativeCompactionHistory(t *testing.T, history codexContextHistory, turnID string) {
	t.Helper()
	found := 0
	for _, turn := range history.Thread.Turns {
		if turn.ID != turnID {
			continue
		}
		require.Equal(t, "completed", turn.Status)
		for _, raw := range turn.Items {
			var item struct{ Type string }
			require.NoError(t, json.Unmarshal(raw, &item))
			if item.Type == "contextCompaction" {
				found++
			}
		}
	}
	require.Equal(t, 1, found, "压缩回合必须在原生历史中保留唯一压缩边界")
}

func runCodexNativeCompaction(t *testing.T, ctx context.Context, client *codex.SocketClient, threadID string) string {
	t.Helper()
	events := client.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	defer events.Close()
	require.NoError(t, client.Call(ctx, "thread/compact/start", map[string]any{"threadId": threadID}, nil))
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	turnID := ""
	started, completed, boundaryStarts, boundaries, legacy := 0, 0, 0, 0, 0
	boundaryID := ""
	warnings := 0
	var quiet <-chan time.Time
	for {
		select {
		case <-quiet:
			require.Equal(t, 1, started)
			require.Equal(t, 1, completed)
			require.Equal(t, 1, boundaryStarts)
			require.Equal(t, 1, boundaries)
			require.Equal(t, 1, warnings, "原生压缩风险提示必须恰好送达一次")
			t.Logf("CONTEXT-007 native contextCompaction=1 legacy thread/compacted=%d", legacy)
			return turnID
		case <-ctx.Done():
			t.Fatalf("原生压缩事件不足: started=%d completed=%d contextCompaction=%d thread/compacted=%d", started, completed, boundaries, legacy)
		case event, ok := <-events.Events():
			require.True(t, ok)
			var params struct {
				ThreadID, TurnID string
				Message          string
				Turn             struct{ ID, Status string }
				Item             struct{ Type, ID string }
			}
			require.NoError(t, json.Unmarshal(event.Params, &params))
			switch event.Method {
			case "turn/started":
				started++
				turnID = params.Turn.ID
				require.NotEmpty(t, turnID)
			case "turn/completed":
				require.Equal(t, 1, warnings, "压缩风险提示必须在回合结束前送达")
				completed++
				require.Equal(t, turnID, params.Turn.ID)
				require.Equal(t, "completed", params.Turn.Status)
			case "item/completed":
				if params.Item.Type == "contextCompaction" {
					require.Equal(t, turnID, params.TurnID)
					require.Equal(t, boundaryID, params.Item.ID)
					boundaries++
				}
			case "item/started":
				if params.Item.Type == "contextCompaction" {
					require.Equal(t, turnID, params.TurnID)
					boundaryID = params.Item.ID
					require.NotEmpty(t, boundaryID)
					boundaryStarts++
				}
			case "thread/compacted":
				require.Equal(t, threadID, params.ThreadID)
				require.Equal(t, turnID, params.TurnID)
				legacy++
			case "warning":
				if strings.HasPrefix(params.Message, "Heads up: Long threads and multiple compactions") {
					require.Equal(t, threadID, params.ThreadID, "风险提示必须关联真实压缩会话")
					require.Equal(t, 1, boundaries, "风险提示必须来自已完成的原生压缩")
					warnings++
				}
			}
			if completed > 0 && boundaries > 0 && quiet == nil {
				quiet = time.After(100 * time.Millisecond)
			}
		}
	}
}
