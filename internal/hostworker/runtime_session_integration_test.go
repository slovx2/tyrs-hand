//go:build integration

package hostworker

import (
	"context"
	"fmt"
	"net"
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

type runtimeSessionFixture struct {
	started, release chan struct{}
	once             sync.Once
	calls            atomic.Int64
}

func (f *runtimeSessionFixture) unblock() { f.once.Do(func() { close(f.release) }) }

func (f *runtimeSessionFixture) model(t *testing.T, ctx context.Context, body []byte) {
	switch f.calls.Add(1) {
	case 1:
		if !strings.Contains(string(body), "SESSION_DISCONNECT") {
			t.Error("首轮模型请求未包含用户输入")
		}
		close(f.started)
		select {
		case <-f.release:
		case <-ctx.Done():
			t.Error("客户端离线不应取消后台模型请求")
		}
	case 2:
		for _, expected := range []string{"SESSION_DISCONNECT", "CLAUDE_OK", "SESSION_FORK"} {
			if !strings.Contains(string(body), expected) {
				t.Errorf("原生 fork 上下文缺少 %s", expected)
			}
		}
	default:
		t.Error("会话管理或重连触发了额外模型请求")
	}
}

type sessionThread struct {
	ID, Name, ForkedFromID string
	GitInfo                map[string]any `json:"gitInfo"`
	Turns                  []struct {
		ID, Status string
	} `json:"turns"`
}

func readSessionThread(t *testing.T, ctx context.Context, client *codex.SocketClient, method string, params map[string]any) sessionThread {
	t.Helper()
	var result struct {
		Thread sessionThread `json:"thread"`
	}
	require.NoError(t, client.Call(ctx, method, params, &result))
	return result.Thread
}

func waitSessionTurn(t *testing.T, ctx context.Context, client *codex.SocketClient, threadID, turnID string) sessionThread {
	t.Helper()
	for {
		thread := readSessionThread(t, ctx, client, "thread/read", map[string]any{"threadId": threadID, "includeTurns": true})
		for _, turn := range thread.Turns {
			if turn.ID == turnID && turn.Status != "inProgress" {
				require.Equal(t, "completed", turn.Status)
				return thread
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal("重连后的原生 Turn 未终结")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func verifyClaudeSessionLifecycle(t *testing.T, ctx context.Context, registry *RuntimeRegistry, connection *ssh.Client, client *codex.SocketClient, signer ssh.Signer, fixture *runtimeSessionFixture) {
	t.Helper()
	entry := registry.entries[runtimeidentity.Claude]
	thread := readSessionThread(t, ctx, client, "thread/start", map[string]any{
		"cwd": entry.Runtime.WorkspaceRoot(), "approvalPolicy": "never", "sandbox": "danger-full-access",
	})
	id := thread.ID
	var ignored any
	require.NoError(t, client.Call(ctx, "thread/name/set", map[string]any{"threadId": id, "name": "SSH 生命周期"}, &ignored))
	git := map[string]any{"sha": strings.Repeat("a", 40), "branch": "main", "originUrl": "/local/bare.git"}
	patched := readSessionThread(t, ctx, client, "thread/metadata/update", map[string]any{"threadId": id, "gitInfo": git})
	require.Equal(t, git, patched.GitInfo)
	goalCall := func(client *codex.SocketClient, method, threadID string, extra map[string]any) map[string]any {
		params := map[string]any{"threadId": threadID}
		for key, value := range extra {
			params[key] = value
		}
		var result map[string]any
		require.NoError(t, client.Call(ctx, method, params, &result))
		return result
	}
	goal := goalCall(client, "thread/goal/set", id, map[string]any{
		"objective": "SSH 暂停目标", "status": "paused", "tokenBudget": 2000,
	})["goal"]
	var started struct {
		Turn struct{ ID string } `json:"turn"`
	}
	require.NoError(t, client.Call(ctx, "turn/start", map[string]any{"threadId": id, "clientUserMessageId": "offline-original",
		"input": []map[string]any{{"type": "text", "text": "SESSION_DISCONNECT"}}}, &started))
	select {
	case <-fixture.started:
	case <-ctx.Done():
		t.Fatal("模型请求未开始")
	}
	require.Error(t, client.Call(ctx, "thread/delete", map[string]any{"threadId": id}, &ignored), "不能删除活动原生会话")
	require.NoError(t, client.Close())
	require.NoError(t, connection.Close(), "必须真正关闭 SSH 连接")
	fixture.unblock()
	reconnect := func() *codex.SocketClient {
		sshClient, err := ssh.Dial("tcp", entry.SSH.Addr().String(), &ssh.ClientConfig{
			User: "test", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, Timeout: 5 * time.Second,
			HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
				if ssh.FingerprintSHA256(key) != entry.SSH.HostKeyFingerprint() {
					return fmt.Errorf("重连 Host Key 不匹配")
				}
				return nil
			},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = sshClient.Close() })
		return connectRuntimeSSH(t, ctx, sshClient, runtimeidentity.Claude)
	}
	client = reconnect()
	resumed := readSessionThread(t, ctx, client, "thread/resume", map[string]any{"threadId": id})
	require.Equal(t, "SSH 生命周期", resumed.Name)
	require.Equal(t, git, resumed.GitInfo)
	completed := waitSessionTurn(t, ctx, client, id, started.Turn.ID)
	require.Len(t, completed.Turns, 1, "断开和重连不能创建重复 Turn")
	fork := readSessionThread(t, ctx, client, "thread/fork", map[string]any{"threadId": id})
	require.NotEqual(t, id, fork.ID)
	require.Equal(t, id, fork.ForkedFromID)
	forkGoal := goalCall(client, "thread/goal/get", fork.ID, nil)["goal"].(map[string]any)
	require.Equal(t, fork.ID, forkGoal["threadId"])
	require.Equal(t, "SSH 暂停目标", forkGoal["objective"])
	goalCall(client, "thread/goal/set", fork.ID, map[string]any{"objective": "独立分支目标", "tokenBudget": nil})
	require.Equal(t, goal, goalCall(client, "thread/goal/get", id, nil)["goal"])
	require.NoError(t, client.Call(ctx, "turn/start", map[string]any{"threadId": fork.ID,
		"input": []map[string]any{{"type": "text", "text": "SESSION_FORK"}}}, &started))
	require.Len(t, waitSessionTurn(t, ctx, client, fork.ID, started.Turn.ID).Turns, 2)
	require.Len(t, readSessionThread(t, ctx, client, "thread/read", map[string]any{"threadId": id, "includeTurns": true}).Turns, 1)
	require.NoError(t, client.Call(ctx, "thread/archive", map[string]any{"threadId": id}, &ignored))
	require.Equal(t, goal, goalCall(client, "thread/goal/get", id, nil)["goal"])
	for _, archived := range []bool{false, true} {
		var listed struct{ Data []sessionThread }
		require.NoError(t, client.Call(ctx, "thread/list", map[string]any{"archived": archived}, &listed))
		found := false
		for _, item := range listed.Data {
			found = found || item.ID == id
		}
		require.Equal(t, archived, found)
	}
	require.Equal(t, git, readSessionThread(t, ctx, client, "thread/unarchive", map[string]any{"threadId": id}).GitInfo)
	var unsubscribed struct{ Status string }
	require.NoError(t, client.Call(ctx, "thread/unsubscribe", map[string]any{"threadId": id}, &unsubscribed))
	require.Equal(t, "unsubscribed", unsubscribed.Status)
	var loaded struct{ Data []string }
	require.NoError(t, client.Call(ctx, "thread/loaded/list", map[string]any{}, &loaded))
	require.Contains(t, loaded.Data, id, "Worker 仍持有普通会话的原生订阅")
	quiet := client.Subscribe(codex.ThreadFilter{ThreadID: id})
	defer quiet.Close()
	observer := reconnect()
	readSessionThread(t, ctx, observer, "thread/resume", map[string]any{"threadId": id})
	observed := observer.Subscribe(codex.ThreadFilter{ThreadID: id})
	defer observed.Close()
	require.NoError(t, observer.Call(ctx, "thread/name/set", map[string]any{"threadId": id, "name": "SSH 生命周期"}, &ignored))
	for renamed := false; !renamed; {
		select {
		case event := <-observed.Events():
			if event.Method == "thread/tokenUsage/updated" {
				// 恢复的用量快照可能在订阅建立后送达，随后仍须收到名称事件。
				continue
			}
			require.Equal(t, "thread/name/updated", event.Method)
			renamed = true
		case <-ctx.Done():
			t.Fatal("另一端的订阅被错误取消")
		}
	}
	select {
	case event := <-quiet.Events():
		t.Fatalf("已取消订阅的端仍收到会话事件: %s", event.Method)
	case <-time.After(100 * time.Millisecond):
	}
	before := registry.entries[runtimeidentity.Codex].Runtime.Generation()
	require.NoError(t, registry.Restart(runtimeidentity.Claude))
	client = reconnect()
	resumed = readSessionThread(t, ctx, client, "thread/resume", map[string]any{"threadId": id})
	require.Equal(t, git, resumed.GitInfo)
	require.Equal(t, "SSH 生命周期", resumed.Name)
	require.Equal(t, goal, goalCall(client, "thread/goal/get", id, nil)["goal"])
	require.NoError(t, client.Call(ctx, "thread/delete", map[string]any{"threadId": id}, &ignored))
	forkGoal = goalCall(client, "thread/goal/get", fork.ID, nil)["goal"].(map[string]any)
	require.Equal(t, "独立分支目标", forkGoal["objective"])
	require.Nil(t, forkGoal["tokenBudget"])
	readSessionThread(t, ctx, client, "thread/resume", map[string]any{"threadId": fork.ID})
	goalEvents := client.Subscribe(codex.ThreadFilter{ThreadID: fork.ID})
	defer goalEvents.Close()
	require.Equal(t, true, goalCall(client, "thread/goal/clear", fork.ID, nil)["cleared"])
	for cleared := false; !cleared; {
		select {
		case event := <-goalEvents.Events():
			cleared = event.Method == "thread/goal/cleared"
		case <-ctx.Done():
			t.Fatal("目标清除后未通知已订阅的 SSH 客户端")
		}
	}
	require.Equal(t, false, goalCall(client, "thread/goal/clear", fork.ID, nil)["cleared"])
	require.NoError(t, client.Call(ctx, "thread/loaded/list", map[string]any{}, &loaded))
	require.NotContains(t, loaded.Data, id)
	require.NoError(t, registry.Restart(runtimeidentity.Claude))
	client = reconnect()
	require.Error(t, client.Call(ctx, "thread/read", map[string]any{"threadId": id, "includeTurns": true}, &ignored))
	require.Error(t, client.Call(ctx, "thread/goal/get", map[string]any{"threadId": id}, &ignored))
	require.Nil(t, goalCall(client, "thread/goal/get", fork.ID, nil)["goal"])
	require.Len(t, readSessionThread(t, ctx, client, "thread/read", map[string]any{"threadId": fork.ID, "includeTurns": true}).Turns, 2)
	require.Equal(t, int64(2), fixture.calls.Load())
	require.Equal(t, before, registry.entries[runtimeidentity.Codex].Runtime.Generation())
}
