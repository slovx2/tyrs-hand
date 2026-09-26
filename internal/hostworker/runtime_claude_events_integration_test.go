//go:build integration

package hostworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestRuntimeClaudeEventsRealSSH(t *testing.T) {
	testRuntimeRegistryRealSSH(t, "claude-events")
}

type runtimeClaudeEventsFixture struct{ calls atomic.Int64 }

func (f *runtimeClaudeEventsFixture) model(t *testing.T, w http.ResponseWriter, body []byte) {
	step := f.calls.Add(1)
	require.LessOrEqual(t, step, int64(3), "读取、删除和重启不得额外请求模型")
	require.True(t, strings.Contains(string(body), "EVENTS_009_"), "模型请求必须来自显式验收回合")
	w.Header().Set("Content-Type", "text/event-stream")
	event := func(kind string, value map[string]any) {
		value["type"] = kind
		encoded, err := json.Marshal(value)
		require.NoError(t, err)
		_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, encoded)
		require.NoError(t, err)
		w.(http.Flusher).Flush()
	}
	event("message_start", map[string]any{"message": map[string]any{
		"id": fmt.Sprintf("msg_events_009_%d", step), "type": "message", "role": "assistant",
		"model": "claude-sonnet-4-6", "content": []any{}, "stop_reason": nil,
		"usage": map[string]int{"input_tokens": 10, "output_tokens": 1}}})
	event("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "thinking", "thinking": ""}})
	for _, delta := range []string{"CLAUDE_REASONING_A", "_B"} {
		event("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "thinking_delta", "thinking": delta}})
	}
	event("content_block_stop", map[string]any{"index": 0})
	event("content_block_start", map[string]any{"index": 1, "content_block": map[string]any{"type": "text", "text": ""}})
	for _, delta := range []string{"CLAUDE_EVENT_TEXT_A", "_B"} {
		event("content_block_delta", map[string]any{"index": 1, "delta": map[string]any{"type": "text_delta", "text": delta}})
	}
	event("content_block_stop", map[string]any{"index": 1})
	event("message_delta", map[string]any{"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
		"usage": map[string]int{"output_tokens": 5}})
	event("message_stop", map[string]any{})
}

type claudeEventCapture struct {
	mu     sync.Mutex
	events []codex.Event
}

func (c *claudeEventCapture) record(event codex.Event) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
	return false
}

func (c *claudeEventCapture) snapshot() []codex.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]codex.Event(nil), c.events...)
}

type claudeEventPeer struct {
	client     *codex.SocketClient
	connection *ssh.Client
	trace      *protocolTraceTransport
	capture    *claudeEventCapture
}

func connectClaudeEventPeer(t *testing.T, ctx context.Context, registry *RuntimeRegistry, signer ssh.Signer) *claudeEventPeer {
	t.Helper()
	entry, err := registry.Entry(runtimeidentity.Claude)
	require.NoError(t, err)
	connection, err := ssh.Dial("tcp", entry.SSH.Addr().String(), &ssh.ClientConfig{
		User: "test", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, Timeout: 5 * time.Second,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if ssh.FingerprintSHA256(key) != entry.SSH.HostKeyFingerprint() {
				return fmt.Errorf("Claude 事件验收 SSH Host Key 不匹配")
			}
			return nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = connection.Close() })
	capture := &claudeEventCapture{}
	client, trace := connectRuntimeSSHWithTrace(t, ctx, connection, runtimeidentity.Claude,
		codex.SocketClientOptions{RequestTimeout: 10 * time.Second, NotificationHandler: capture.record})
	return &claudeEventPeer{client: client, connection: connection, trace: trace, capture: capture}
}

func (p *claudeEventPeer) close() {
	p.trace.expectClose("client-disconnect")
	_ = p.client.Close()
	_ = p.connection.Close()
}

type claudeEventTurn struct {
	ID, Status string
	Items      []json.RawMessage
}

func readClaudeEventTurn(t *testing.T, ctx context.Context, client *codex.SocketClient, threadID string) claudeEventTurn {
	t.Helper()
	var result struct {
		Thread struct{ Turns []claudeEventTurn }
	}
	require.NoError(t, client.Call(ctx, "thread/read", map[string]any{"threadId": threadID, "includeTurns": true}, &result))
	require.Len(t, result.Thread.Turns, 1)
	return result.Thread.Turns[0]
}

// EVENTS-009：只从真实 SDK 的 thinking SSE 取得事件，不注入 app-server 通知或历史。
func verifyClaudeEventTurn(t *testing.T, ctx context.Context, peer *claudeEventPeer, threadID, scenario string) claudeEventTurn {
	t.Helper()
	checkpoint := len(peer.capture.snapshot())
	subscription := peer.client.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	defer subscription.Close()
	var started struct{ Turn struct{ ID string } }
	require.NoError(t, peer.client.Call(ctx, "turn/start", map[string]any{"threadId": threadID,
		"input": []map[string]any{{"type": "text", "text": "EVENTS_009_" + scenario}}}, &started))
	for {
		select {
		case <-ctx.Done():
			t.Fatal("Claude 真 SSH 推理事件缺少终态")
		case event, ok := <-subscription.Events():
			require.True(t, ok)
			if event.Method == "turn/completed" {
				goto completed
			}
		}
	}
completed:
	history := readClaudeEventTurn(t, ctx, peer.client, threadID)
	require.Equal(t, started.Turn.ID, history.ID)
	require.Equal(t, "completed", history.Status)
	starts, ends := map[string]string{}, map[string]json.RawMessage{}
	order, completedItems := []string{}, []json.RawMessage{}
	deltas, chunks := map[string]string{}, map[string]int{}
	turnStarts, turnEnds := 0, 0
	for _, event := range peer.capture.snapshot()[checkpoint:] {
		var params struct {
			ThreadID, TurnID, ItemID, Delta string
			ContentIndex                    int
			Item                            json.RawMessage
			Turn                            struct {
				ID, Status string
				Error      any
			}
		}
		require.NoError(t, json.Unmarshal(event.Params, &params))
		if params.ThreadID != threadID {
			continue
		}
		if params.TurnID != "" {
			require.Equal(t, history.ID, params.TurnID)
		}
		switch event.Method {
		case "turn/started":
			turnStarts++
			require.Zero(t, turnEnds)
			require.Equal(t, history.ID, params.Turn.ID)
		case "item/started", "item/completed":
			require.Equal(t, 1, turnStarts)
			require.Zero(t, turnEnds)
			var item struct {
				ID, Type, Text string
				Content        []any
			}
			require.NoError(t, json.Unmarshal(params.Item, &item))
			if event.Method == "item/started" {
				require.NotContains(t, starts, item.ID)
				starts[item.ID] = item.Type
				order = append(order, item.Type)
			} else {
				require.Equal(t, item.Type, starts[item.ID])
				require.NotContains(t, ends, item.ID)
				ends[item.ID] = params.Item
				completedItems = append(completedItems, params.Item)
				if item.Type == "reasoning" {
					require.Equal(t, []any{"CLAUDE_REASONING_A_B"}, item.Content)
				}
				if item.Type == "agentMessage" {
					require.Equal(t, "CLAUDE_EVENT_TEXT_A_B", item.Text)
				}
			}
		case "item/reasoning/textDelta", "item/agentMessage/delta":
			require.Zero(t, turnEnds)
			require.Equal(t, map[string]string{"item/reasoning/textDelta": "reasoning", "item/agentMessage/delta": "agentMessage"}[event.Method], starts[params.ItemID])
			require.NotContains(t, ends, params.ItemID)
			require.Zero(t, params.ContentIndex)
			deltas[event.Method] += params.Delta
			chunks[event.Method]++
		case "turn/completed":
			turnEnds++
			require.Equal(t, history.ID, params.Turn.ID)
			require.Equal(t, "completed", params.Turn.Status)
			require.Nil(t, params.Turn.Error)
			require.Len(t, ends, len(starts))
		}
	}
	require.Equal(t, 1, turnStarts)
	require.Equal(t, 1, turnEnds)
	require.Equal(t, []string{"reasoning", "agentMessage"}, order)
	require.Equal(t, map[string]int{"item/reasoning/textDelta": 2, "item/agentMessage/delta": 2}, chunks)
	require.Equal(t, map[string]string{"item/reasoning/textDelta": "CLAUDE_REASONING_A_B", "item/agentMessage/delta": "CLAUDE_EVENT_TEXT_A_B"}, deltas)
	// 用户输入保存在历史；推理和回答还必须与实时 Item 事件逐项一致。
	require.Len(t, history.Items, len(completedItems)+1)
	var input struct {
		ID, Type string
		Content  []struct{ Type, Text string }
	}
	require.NoError(t, json.Unmarshal(history.Items[0], &input))
	require.NotEmpty(t, input.ID)
	require.Equal(t, "userMessage", input.Type)
	require.Len(t, input.Content, 1)
	require.Equal(t, "text", input.Content[0].Type)
	require.Equal(t, "EVENTS_009_"+scenario, input.Content[0].Text)
	for index, item := range completedItems {
		require.JSONEq(t, string(item), string(history.Items[index+1]), "持久历史必须保留实时 Item ID、顺序及内容")
	}
	return history
}

func claudeEventCount(events []codex.Event, method, threadID string) int {
	count := 0
	for _, event := range events {
		var params struct{ ThreadID string }
		if event.Method == method && json.Unmarshal(event.Params, &params) == nil && params.ThreadID == threadID {
			count++
		}
	}
	return count
}

func requireClaudeThreadDeleted(t *testing.T, ctx context.Context, client *codex.SocketClient, threadID string) {
	t.Helper()
	for _, archived := range []bool{false, true} {
		var list struct {
			Data       []struct{ ID string }
			NextCursor *string
		}
		require.NoError(t, client.Call(ctx, "thread/list", map[string]any{"archived": archived, "limit": 100}, &list))
		require.Nil(t, list.NextCursor)
		for _, item := range list.Data {
			require.NotEqual(t, threadID, item.ID, "删除不能仅发送通知，列表必须真正移除会话")
		}
	}
	var ignored any
	err := client.Call(ctx, "thread/read", map[string]any{"threadId": threadID, "includeTurns": true}, &ignored)
	var rpcErr *codex.RPCError
	require.True(t, errors.As(err, &rpcErr), "删除后读取必须返回明确 RPC 错误")
	require.Equal(t, -32602, rpcErr.Code)
}

func deleteClaudeEventThread(t *testing.T, ctx context.Context, caller *claudeEventPeer, threadID, scenario string, loadedPeer *claudeEventPeer) {
	t.Helper()
	checkpoint := len(caller.capture.snapshot())
	peerCheckpoint := 0
	if loadedPeer != nil {
		peerCheckpoint = len(loadedPeer.capture.snapshot())
	}
	var ignored any
	require.NoError(t, caller.client.Call(ctx, "thread/delete", map[string]any{"threadId": threadID}, &ignored))
	// 先核实真实删除副作用，再等待调用者通知，以区分路由丢失和删除失败。
	requireClaudeThreadDeleted(t, ctx, caller.client, threadID)
	t.Logf("EVENTS-009 scenario=%s deleteRPC=success listAbsent=true readError=-32602", scenario)
	require.Eventually(t, func() bool {
		return claudeEventCount(caller.capture.snapshot()[checkpoint:], "thread/deleted", threadID) > 0
	}, 2*time.Second, 10*time.Millisecond, "scenario=%s 真 SSH 调用者未收到 thread/deleted", scenario)
	requireClaudeThreadDeleted(t, ctx, caller.client, threadID)
	require.Equal(t, 1, claudeEventCount(caller.capture.snapshot()[checkpoint:], "thread/deleted", threadID))
	if loadedPeer != nil {
		require.Eventually(t, func() bool {
			return claudeEventCount(loadedPeer.capture.snapshot()[peerCheckpoint:], "thread/deleted", threadID) > 0
		}, 2*time.Second, 10*time.Millisecond)
		requireClaudeThreadDeleted(t, ctx, loadedPeer.client, threadID)
		for _, events := range [][]codex.Event{caller.capture.snapshot()[checkpoint:], loadedPeer.capture.snapshot()[peerCheckpoint:]} {
			require.Equal(t, 1, claudeEventCount(events, "thread/deleted", threadID))
			require.Equal(t, 1, claudeEventCount(events, "thread/closed", threadID))
		}
	}
	err := caller.client.Call(ctx, "thread/delete", map[string]any{"threadId": threadID}, &ignored)
	var rpcErr *codex.RPCError
	require.True(t, errors.As(err, &rpcErr))
	require.Equal(t, -32602, rpcErr.Code)
	requireClaudeThreadDeleted(t, ctx, caller.client, threadID)
	require.Equal(t, 1, claudeEventCount(caller.capture.snapshot()[checkpoint:], "thread/deleted", threadID), "重复删除失败不能再发送成功事件")
}

func verifyRuntimeClaudeEvents(t *testing.T, ctx context.Context, registry *RuntimeRegistry, signer ssh.Signer, fixture *runtimeClaudeEventsFixture, root string) {
	t.Helper()
	codexGeneration := registry.entries[runtimeidentity.Codex].Runtime.Generation()
	primary := connectClaudeEventPeer(t, ctx, registry, signer)
	defer func() { primary.close() }()
	deletedIDs := []string{}
	for _, scenario := range []string{"loaded", "unsubscribed", "restarted"} {
		if !t.Run(scenario, func(t *testing.T) {
			thread := readSessionThread(t, ctx, primary.client, "thread/start", map[string]any{
				"cwd": filepath.Join(root, "project"), "approvalPolicy": "never", "sandbox": "danger-full-access"})
			history := verifyClaudeEventTurn(t, ctx, primary, thread.ID, scenario)
			t.Logf("EVENTS-009 scenario=%s reasoningDeltas=2 textDeltas=2 items=3 uniqueTerminals=true historyVerified=true", scenario)
			switch scenario {
			case "loaded":
				caller := connectClaudeEventPeer(t, ctx, registry, signer)
				defer caller.close()
				readSessionThread(t, ctx, caller.client, "thread/resume", map[string]any{"threadId": thread.ID})
				deleteClaudeEventThread(t, ctx, caller, thread.ID, scenario, primary)
			case "unsubscribed":
				var result struct{ Status string }
				require.NoError(t, primary.client.Call(ctx, "thread/unsubscribe", map[string]any{"threadId": thread.ID}, &result))
				require.Equal(t, "unsubscribed", result.Status)
				deleteClaudeEventThread(t, ctx, primary, thread.ID, scenario, nil)
			case "restarted":
				primary.close()
				require.NoError(t, registry.Restart(runtimeidentity.Claude))
				primary = connectClaudeEventPeer(t, ctx, registry, signer)
				require.Equal(t, history, readClaudeEventTurn(t, ctx, primary.client, thread.ID))
				var loaded struct{ Data []string }
				require.NoError(t, primary.client.Call(ctx, "thread/loaded/list", map[string]any{}, &loaded))
				require.NotContains(t, loaded.Data, thread.ID, "只读历史不能隐式恢复会话订阅")
				deleteClaudeEventThread(t, ctx, primary, thread.ID, scenario, nil)
			}
			deletedIDs = append(deletedIDs, thread.ID)
			if directory := os.Getenv("PROTOCOL_ARTIFACT_DIR"); directory != "" {
				data, err := json.MarshalIndent(map[string]any{"formatVersion": 1, "runId": os.Getenv("PROTOCOL_RUN_ID"),
					"engine": runtimeidentity.Claude, "caseName": "TestRuntimeClaudeEventsRealSSH", "caseIds": []string{"EVENTS-009"}, "kind": "event-effects",
					"payload": map[string]any{"scenario": scenario, "threadId": thread.ID, "turnId": history.ID,
						"reasoningDeltas": 2, "textDeltas": 2, "items": 3, "uniqueTerminals": true, "historyVerified": true, "deleted": true}}, "", "  ")
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(directory, "claude-event-effects-"+scenario+".json"), data, 0o600))
			}
		}) {
			return
		}
	}
	primary.close()
	require.NoError(t, registry.Restart(runtimeidentity.Claude))
	primary = connectClaudeEventPeer(t, ctx, registry, signer)
	for _, threadID := range deletedIDs {
		requireClaudeThreadDeleted(t, ctx, primary.client, threadID)
	}
	require.Len(t, deletedIDs, 3)
	require.Equal(t, int64(3), fixture.calls.Load())
	require.Equal(t, codexGeneration, registry.entries[runtimeidentity.Codex].Runtime.Generation())
	if directory := os.Getenv("PROTOCOL_ARTIFACT_DIR"); directory != "" {
		data, err := json.MarshalIndent(map[string]any{"formatVersion": 1, "runId": os.Getenv("PROTOCOL_RUN_ID"),
			"engine": runtimeidentity.Claude, "caseName": "TestRuntimeClaudeEventsRealSSH", "caseIds": []string{"EVENTS-009"}, "kind": "event-effects",
			"payload": map[string]any{"scenario": "final-restart", "deletedThreadIds": deletedIDs,
				"listAndReadAbsent": true, "modelCalls": fixture.calls.Load(), "codexGenerationUnchanged": true}}, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(directory, "claude-event-effects-final-restart.json"), data, 0o600))
	}
}
