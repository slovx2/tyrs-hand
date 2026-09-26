//go:build integration

package hostworker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestRuntimeHooksRealSSH(t *testing.T) {
	testRuntimeRegistryRealSSH(t, "hooks")
}

type runtimeHookItem struct {
	ID, Type  string
	Fragments []struct{ Text, HookRunID string }
}

// HOOKS-004：真实 SSH 目录与原生 CLI 执行对账，禁用必须阻止文件副作用。
func verifyRuntimeHooks(t *testing.T, ctx context.Context, registry *RuntimeRegistry,
	connections map[runtimeidentity.Engine]*ssh.Client, clients map[runtimeidentity.Engine]*codex.SocketClient, root string,
) {
	t.Helper()
	cwd := filepath.Join(root, "project")
	settingsPath := filepath.Join(cwd, ".claude", "settings.json")
	effect := filepath.Join(cwd, "hook-effects.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(settingsPath), 0o700))
	hooks := map[string]any{}
	for _, event := range []string{"SessionStart", "Stop"} {
		hooks[event] = []any{map[string]any{"hooks": []any{map[string]any{
			"type": "command", "timeout": 5,
			"command": fmt.Sprintf("printf '%s\\n' >> '%s'; printf 'SSH_HOOK_%s'", event, effect, event),
		}}}}
	}
	writeSettings := func(disabled bool) {
		data, err := json.Marshal(map[string]any{"hooks": hooks, "disableAllHooks": disabled})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(settingsPath, data, 0o600))
	}
	writeSettings(false)
	client := clients[runtimeidentity.Claude]
	var completed []runtimeHookItem
	var originalThread string
	for _, disabled := range []bool{false, true} {
		if disabled {
			writeSettings(true)
			require.NoError(t, registry.Restart(runtimeidentity.Claude))
			client = connectRuntimeSSH(t, ctx, connections[runtimeidentity.Claude], runtimeidentity.Claude)
		}
		var listing struct {
			Data []struct {
				Cwd   string
				Hooks []struct {
					SourcePath, EventName string
					Enabled               bool
				}
				Errors []json.RawMessage
			}
		}
		require.NoError(t, client.Call(ctx, "hooks/list", map[string]any{"cwds": []string{cwd}}, &listing))
		require.Len(t, listing.Data, 1)
		require.Equal(t, cwd, listing.Data[0].Cwd)
		require.Empty(t, listing.Data[0].Errors)
		require.Len(t, listing.Data[0].Hooks, 2)
		for _, hook := range listing.Data[0].Hooks {
			require.Equal(t, settingsPath, hook.SourcePath)
			require.Equal(t, !disabled, hook.Enabled)
		}
		thread := readSessionThread(t, ctx, client, "thread/start", map[string]any{
			"cwd": cwd, "approvalPolicy": "never", "sandbox": "danger-full-access",
		})
		events := client.Subscribe(codex.ThreadFilter{ThreadID: thread.ID})
		var started struct{ Turn struct{ ID string } }
		require.NoError(t, client.Call(ctx, "turn/start", map[string]any{
			"threadId": thread.ID, "input": []map[string]any{{"type": "text", "text": "验证真实 Hook"}},
		}, &started))
		starts, ends := map[string]runtimeHookItem{}, map[string]runtimeHookItem{}
		finished := false
		for !finished {
			select {
			case <-ctx.Done():
				t.Fatal("Hook 真 SSH 回合未终结")
			case event, ok := <-events.Events():
				require.True(t, ok)
				var params struct {
					ThreadID, TurnID string
					Item             runtimeHookItem
					Turn             struct{ ID, Status string }
				}
				require.NoError(t, json.Unmarshal(event.Params, &params))
				if params.Item.Type == "hookPrompt" {
					require.Equal(t, thread.ID, params.ThreadID)
					require.Equal(t, started.Turn.ID, params.TurnID)
					if event.Method == "item/started" {
						require.NotContains(t, starts, params.Item.ID)
						starts[params.Item.ID] = params.Item
					}
					if event.Method == "item/completed" {
						require.Contains(t, starts, params.Item.ID)
						require.NotContains(t, ends, params.Item.ID)
						require.NotEmpty(t, params.Item.Fragments)
						hookID := params.Item.Fragments[0].HookRunID
						require.NotEmpty(t, hookID)
						require.Equal(t, starts[params.Item.ID].Fragments[0].HookRunID, hookID)
						var text strings.Builder
						for _, fragment := range params.Item.Fragments {
							require.Equal(t, hookID, fragment.HookRunID)
							text.WriteString(fragment.Text)
						}
						require.Contains(t, text.String(), "结果: success")
						require.Contains(t, text.String(), "SSH_HOOK_")
						ends[params.Item.ID] = params.Item
						completed = append(completed, params.Item)
					}
				}
				if event.Method == "turn/completed" {
					require.Equal(t, started.Turn.ID, params.Turn.ID)
					require.Equal(t, "completed", params.Turn.Status)
					finished = true
				}
			}
		}
		events.Close()
		if disabled {
			require.Empty(t, starts)
			require.Empty(t, ends)
		} else {
			require.Len(t, starts, 2)
			require.Len(t, ends, 2)
			originalThread = thread.ID
		}
		data, err := os.ReadFile(effect)
		require.NoError(t, err)
		require.Equal(t, "SessionStart\nStop\n", string(data), "禁用后不能新增 Hook 副作用")
	}
	var history struct {
		Thread struct {
			Turns []struct{ Items []runtimeHookItem }
		}
	}
	require.NoError(t, client.Call(ctx, "thread/read", map[string]any{
		"threadId": originalThread, "includeTurns": true,
	}, &history))
	require.Len(t, history.Thread.Turns, 1)
	var stored []runtimeHookItem
	for _, item := range history.Thread.Turns[0].Items {
		if item.Type == "hookPrompt" {
			stored = append(stored, item)
		}
	}
	require.Equal(t, completed, stored, "重启恢复的 Hook 历史必须与原始事件一致")
}
