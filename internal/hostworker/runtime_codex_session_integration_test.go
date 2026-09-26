//go:build integration

package hostworker

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func (f *runtimeSessionFixture) codexModel(t *testing.T, ctx context.Context, body []byte) {
	switch f.calls.Add(1) {
	case 1:
		if !strings.Contains(string(body), "SESSION_DISCONNECT") {
			t.Error("Codex 模型缺少用户输入")
		}
		close(f.started)
		select {
		case <-f.release:
		case <-ctx.Done():
			t.Error("SSH 离线不能取消原生模型请求")
		}
	case 2:
		for _, expected := range []string{"SESSION_DISCONNECT", "CODEX_OK", "SESSION_FORK"} {
			if !strings.Contains(string(body), expected) {
				t.Errorf("Codex 原生分叉上下文缺少 %s", expected)
			}
		}
	case 3:
		for _, expected := range []string{"SESSION_DISCONNECT", "CODEX_OK", "AFTER_ROLLBACK"} {
			if !strings.Contains(string(body), expected) {
				t.Errorf("Codex 回退后上下文缺少 %s", expected)
			}
		}
		if strings.Contains(string(body), "SESSION_FORK") {
			t.Error("回退删除的轮次仍进入模型请求")
		}
	default:
		t.Error("Codex 管理协议触发了额外模型请求")
	}
}

// SESSION-003：使用真实 Codex、Hub 和两次 SSH 连接，不预制 rollout 或历史。
func verifyCodexSessionLifecycle(t *testing.T, ctx context.Context, registry *RuntimeRegistry, connection *ssh.Client, client *codex.SocketClient, signer ssh.Signer, fixture *runtimeSessionFixture) {
	t.Helper()
	entry := registry.entries[runtimeidentity.Codex]
	otherGeneration := registry.entries[runtimeidentity.Claude].Runtime.Generation()
	reconnect := func() *codex.SocketClient {
		sshClient, err := ssh.Dial("tcp", entry.SSH.Addr().String(), &ssh.ClientConfig{
			User: "test", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, Timeout: 5 * time.Second,
			HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
				if ssh.FingerprintSHA256(key) != entry.SSH.HostKeyFingerprint() {
					return fmt.Errorf("Codex 重连 Host Key 不匹配")
				}
				return nil
			},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = sshClient.Close() })
		return connectRuntimeSSH(t, ctx, sshClient, runtimeidentity.Codex)
	}
	thread := readSessionThread(t, ctx, client, "thread/start", map[string]any{
		"cwd": entry.Runtime.WorkspaceRoot(), "approvalPolicy": "never", "sandbox": "danger-full-access",
	})
	start := func(client *codex.SocketClient, threadID, text string) string {
		var result struct{ Turn struct{ ID string } }
		require.NoError(t, client.Call(ctx, "turn/start", map[string]any{
			"threadId": threadID, "clientUserMessageId": text,
			"input": []map[string]any{{"type": "text", "text": text}},
		}, &result))
		return result.Turn.ID
	}
	first := start(client, thread.ID, "SESSION_DISCONNECT")
	select {
	case <-fixture.started:
	case <-ctx.Done():
		t.Fatal("Codex 原生请求未开始")
	}
	require.NoError(t, client.Close())
	require.NoError(t, connection.Close())
	fixture.unblock()
	client = reconnect()
	complete := func(threadID, text string) sessionThread {
		events := client.Subscribe(codex.ThreadFilter{ThreadID: threadID})
		defer events.Close()
		turnID := start(client, threadID, text)
		for {
			select {
			case <-ctx.Done():
				t.Fatal("Codex 会话回合缺少终态事件")
			case event := <-events.Events():
				if event.Method != "turn/completed" {
					continue
				}
				var result struct{ Turn struct{ ID, Status string } }
				require.NoError(t, json.Unmarshal(event.Params, &result))
				if result.Turn.ID != turnID {
					continue
				}
				require.Equal(t, "completed", result.Turn.Status, string(event.Params))
				return readSessionThread(t, ctx, client, "thread/read", map[string]any{"threadId": threadID, "includeTurns": true})
			}
		}
	}
	readSessionThread(t, ctx, client, "thread/resume", map[string]any{"threadId": thread.ID})
	require.Len(t, waitSessionTurn(t, ctx, client, thread.ID, first).Turns, 1)
	require.NoError(t, client.Call(ctx, "thread/name/set", map[string]any{"threadId": thread.ID, "name": "Codex SSH 回归"}, nil))
	git := map[string]any{"sha": strings.Repeat("a", 40), "branch": "main", "originUrl": "/local/bare.git"}
	require.Equal(t, git, readSessionThread(t, ctx, client, "thread/metadata/update", map[string]any{"threadId": thread.ID, "gitInfo": git}).GitInfo)
	fork := readSessionThread(t, ctx, client, "thread/fork", map[string]any{"threadId": thread.ID})
	require.NotEqual(t, thread.ID, fork.ID)
	require.Equal(t, thread.ID, fork.ForkedFromID)
	require.Len(t, complete(fork.ID, "SESSION_FORK").Turns, 2)
	rolled := readSessionThread(t, ctx, client, "thread/rollback", map[string]any{"threadId": fork.ID, "numTurns": 1})
	require.Len(t, rolled.Turns, 1)
	require.Len(t, complete(fork.ID, "AFTER_ROLLBACK").Turns, 2)
	require.Len(t, readSessionThread(t, ctx, client, "thread/read", map[string]any{"threadId": thread.ID, "includeTurns": true}).Turns, 1)
	require.NoError(t, client.Call(ctx, "thread/archive", map[string]any{"threadId": thread.ID}, nil))
	for _, archived := range []bool{false, true} {
		var listed struct{ Data []sessionThread }
		require.NoError(t, client.Call(ctx, "thread/list", map[string]any{"archived": archived}, &listed))
		found := false
		for _, row := range listed.Data {
			found = found || row.ID == thread.ID
		}
		require.Equal(t, archived, found)
	}
	readSessionThread(t, ctx, client, "thread/unarchive", map[string]any{"threadId": thread.ID})
	var unsubscribed struct{ Status string }
	require.NoError(t, client.Call(ctx, "thread/unsubscribe", map[string]any{"threadId": fork.ID}, &unsubscribed))
	require.Equal(t, "unsubscribed", unsubscribed.Status)
	var loaded struct{ Data []string }
	require.NoError(t, client.Call(ctx, "thread/loaded/list", map[string]any{}, &loaded))
	require.Contains(t, loaded.Data, fork.ID, "Worker 的后台订阅不能被某客户端取消")
	require.NoError(t, registry.Restart(runtimeidentity.Codex))
	client = reconnect()
	resumed := readSessionThread(t, ctx, client, "thread/resume", map[string]any{"threadId": thread.ID})
	require.Equal(t, "Codex SSH 回归", resumed.Name)
	require.Equal(t, git, resumed.GitInfo)
	require.Len(t, resumed.Turns, 1)
	require.NoError(t, client.Call(ctx, "thread/delete", map[string]any{"threadId": thread.ID}, nil))
	require.Error(t, client.Call(ctx, "thread/read", map[string]any{"threadId": thread.ID, "includeTurns": true}, nil))
	require.Len(t, readSessionThread(t, ctx, client, "thread/read", map[string]any{"threadId": fork.ID, "includeTurns": true}).Turns, 2)
	require.Equal(t, int64(3), fixture.calls.Load())
	require.Equal(t, otherGeneration, registry.entries[runtimeidentity.Claude].Runtime.Generation())
}

func TestRuntimeCodexMetadataRealSSH(t *testing.T) {
	testRuntimeRegistryRealSSH(t, "codex-metadata")
}

type nativeThreadSection struct{ ID, Name string }

func listNativeThreadSections(t *testing.T, ctx context.Context, client *codex.SocketClient) []nativeThreadSection {
	t.Helper()
	var cursor *string
	sections := []nativeThreadSection{}
	seen := map[string]bool{}
	for page := 0; page < 20; page++ {
		result := nativeMetadataCall[struct {
			Data       []nativeThreadSection
			NextCursor *string
		}](t, ctx, client, "threadSection/list", map[string]any{"limit": 1, "cursor": cursor})
		require.LessOrEqual(t, len(result.Data), 1)
		for _, section := range result.Data {
			require.NotEmpty(t, section.ID)
			require.NotEmpty(t, section.Name)
			require.False(t, seen[section.ID], "目录分页不能重复")
			seen[section.ID] = true
			sections = append(sections, section)
		}
		cursor = result.NextCursor
		if cursor == nil {
			break
		}
	}
	require.Nil(t, cursor, "分区分页必须终止")
	return sections
}

func runNativeMetadataTurn(t *testing.T, ctx context.Context, client *codex.SocketClient, threadID, text string) string {
	t.Helper()
	events := client.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	defer events.Close()
	started := nativeMetadataCall[struct{ Turn struct{ ID string } }](t, ctx, client, "turn/start", map[string]any{"threadId": threadID, "input": []map[string]any{{"type": "text", "text": text}}})
	require.NotEmpty(t, started.Turn.ID)
	for {
		select {
		case <-ctx.Done():
			t.Fatal("真实模型回合没有完成事件")
		case event, ok := <-events.Events():
			require.True(t, ok, "SSH 通知流意外关闭")
			if event.Method != "turn/completed" {
				continue
			}
			var completed struct{ Turn struct{ ID, Status string } }
			require.NoError(t, json.Unmarshal(event.Params, &completed))
			if completed.Turn.ID != started.Turn.ID {
				continue
			}
			require.Equal(t, "completed", completed.Turn.Status)
			return completed.Turn.ID
		}
	}
}

// HISTORY-004：两次真实模型回合的正反分页与视图，不能注入 rollout 或会话历史。
func verifyNativeTurnPages(t *testing.T, ctx context.Context, client *codex.SocketClient, threadID string, turns []string) {
	t.Helper()
	for _, direction := range []string{"asc", "desc"} {
		for _, view := range []string{"notLoaded", "summary", "full"} {
			var cursor *string
			found := []string{}
			for page := 0; page < 4; page++ {
				result := nativeMetadataCall[struct {
					Data []struct {
						ID, Status, ItemsView string
						Items                 []json.RawMessage
					}
					NextCursor *string
				}](t, ctx, client, "thread/turns/list", map[string]any{"threadId": threadID, "limit": 1, "cursor": cursor, "sortDirection": direction, "itemsView": view})
				require.Len(t, result.Data, 1)
				turn := result.Data[0]
				require.Equal(t, "completed", turn.Status)
				require.Equal(t, view, turn.ItemsView)
				require.NotContains(t, found, turn.ID)
				found = append(found, turn.ID)
				if view == "notLoaded" {
					require.Empty(t, turn.Items)
				} else {
					require.Len(t, turn.Items, 2)
					expectedInput := "NATIVE_METADATA_ONE"
					if turn.ID == turns[1] {
						expectedInput = "NATIVE_METADATA_TWO"
					}
					require.Contains(t, string(turn.Items[0]), expectedInput)
					require.Contains(t, string(turn.Items[1]), "CODEX_OK")
				}
				cursor = result.NextCursor
				if cursor == nil {
					break
				}
			}
			require.Nil(t, cursor)
			expected := append([]string(nil), turns...)
			if direction == "desc" {
				expected[0], expected[1] = expected[1], expected[0]
			}
			require.Equal(t, expected, found)
		}
	}
}

func verifyNativeSectionMembership(t *testing.T, ctx context.Context, client *codex.SocketClient, threadID string, section nativeThreadSection) {
	t.Helper()
	result := nativeMetadataCall[struct {
		Data []struct {
			ID      string
			Section *nativeThreadSection
		}
		NextCursor *string
	}](t, ctx, client, "thread/list", map[string]any{"sectionId": section.ID, "limit": 10})
	require.Len(t, result.Data, 1)
	require.Nil(t, result.NextCursor)
	require.Equal(t, threadID, result.Data[0].ID)
	require.Equal(t, &section, result.Data[0].Section)
}

// SESSION-005：真实会话分区变更、分页及重启持久，另外验收 HISTORY-004、GOAL-004。
func verifyCodexNativeMetadata(t *testing.T, ctx context.Context, client *codex.SocketClient, root string, registry *RuntimeRegistry, connection *ssh.Client) {
	t.Helper()
	otherGeneration := registry.entries[runtimeidentity.Claude].Runtime.Generation()
	baseline := listNativeThreadSections(t, ctx, client)
	created := []nativeThreadSection{}
	for _, name := range []string{"Native Alpha", "Native Beta", "Native Gamma"} {
		result := nativeMetadataCall[struct{ Section nativeThreadSection }](t, ctx, client, "threadSection/create", map[string]any{"name": name})
		require.Equal(t, name, result.Section.Name)
		require.NotEmpty(t, result.Section.ID)
		created = append(created, result.Section)
	}
	expectedSections := append(append([]nativeThreadSection{}, baseline...), created...)
	require.Equal(t, expectedSections, listNativeThreadSections(t, ctx, client))
	renamed := nativeMetadataCall[struct{ Section nativeThreadSection }](t, ctx, client, "threadSection/update", map[string]any{"sectionId": created[0].ID, "name": "Native Alpha Renamed"})
	require.Equal(t, nativeThreadSection{ID: created[0].ID, Name: "Native Alpha Renamed"}, renamed.Section)
	created[0] = renamed.Section
	nativeMetadataCall[struct{}](t, ctx, client, "threadSection/delete", map[string]any{"sectionId": created[2].ID})
	expectedSections = append(append([]nativeThreadSection{}, baseline...), created[:2]...)
	require.Equal(t, expectedSections, listNativeThreadSections(t, ctx, client))
	thread := readSessionThread(t, ctx, client, "thread/start", map[string]any{"cwd": root, "approvalPolicy": "never", "sandbox": "danger-full-access"})
	turns := []string{runNativeMetadataTurn(t, ctx, client, thread.ID, "NATIVE_METADATA_ONE"), runNativeMetadataTurn(t, ctx, client, thread.ID, "NATIVE_METADATA_TWO")}
	require.NotEqual(t, turns[0], turns[1])
	for _, section := range []nativeThreadSection{created[0], created[1], created[0]} {
		nativeMetadataCall[struct{}](t, ctx, client, "thread/section/move", map[string]any{"threadId": thread.ID, "sectionId": section.ID})
		verifyNativeSectionMembership(t, ctx, client, thread.ID, section)
	}
	other := nativeMetadataCall[struct{ Data []json.RawMessage }](t, ctx, client, "thread/list", map[string]any{"sectionId": created[1].ID})
	require.Empty(t, other.Data, "会话不能留在旧分区")
	verifyNativeTurnPages(t, ctx, client, thread.ID, turns)
	goal := createCodexPausedGoal(t, ctx, client, thread.ID)
	require.NoError(t, registry.Restart(runtimeidentity.Codex))
	client = connectRuntimeSSH(t, ctx, connection, runtimeidentity.Codex)
	require.Equal(t, expectedSections, listNativeThreadSections(t, ctx, client))
	verifyNativeSectionMembership(t, ctx, client, thread.ID, created[0])
	verifyNativeTurnPages(t, ctx, client, thread.ID, turns)
	require.Len(t, readSessionThread(t, ctx, client, "thread/read", map[string]any{"threadId": thread.ID, "includeTurns": true}).Turns, 2)
	verifyCodexPausedGoalAfterRestart(t, ctx, client, goal)
	require.Equal(t, otherGeneration, registry.entries[runtimeidentity.Claude].Runtime.Generation())
}
