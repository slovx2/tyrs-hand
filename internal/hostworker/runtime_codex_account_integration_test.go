//go:build integration

package hostworker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

const runtimeAccountTestKey = "sk-test-account-001-not-a-secret"
const runtimeAccountResetAt int64 = 2_000_000_000

func TestRuntimeCodexAccountRealSSH(t *testing.T) {
	testRuntimeRegistryRealSSH(t, "codex-account")
}

type runtimeCodexAccountFixture struct{ calls atomic.Int64 }

// 必须由 file store 的登录凭据鉴权，不能由 env_key 或宿主环境替代。
func (f *runtimeCodexAccountFixture) configuration(baseURL string) string {
	return fmt.Sprintf("model = \"mock-model\"\nmodel_provider = \"mock\"\napproval_policy = \"never\"\ncli_auth_credentials_store = \"file\"\nchatgpt_base_url = %q\n[model_providers.mock]\nname = \"Mock\"\nbase_url = %q\nwire_api = \"responses\"\nrequires_openai_auth = true\nrequest_max_retries = 0\nstream_max_retries = 0\nsupports_websockets = false\n", baseURL, baseURL+"/v1")
}

func (f *runtimeCodexAccountFixture) model(t *testing.T, w http.ResponseWriter, request *http.Request, body []byte) {
	t.Helper()
	step := f.calls.Add(1)
	require.Equal(t, "/v1/responses", request.URL.Path, "账户专项只能调用本地 Codex Responses")
	require.LessOrEqual(t, step, int64(2), "账户管理和重启不能调用模型")
	require.True(t, request.Header.Get("Authorization") == "Bearer "+runtimeAccountTestKey, "模型请求必须使用真实登录写入的虚拟凭据")
	require.Contains(t, string(body), "ACCOUNT_001_BEFORE_RESTART")
	if step == 2 {
		require.Contains(t, string(body), "ACCOUNT_001_AFTER_RESTART")
	}
	used := int64(17)
	if step == 2 {
		used = 29
	}
	w.Header().Set("x-codex-primary-used-percent", strconv.FormatInt(used, 10))
	w.Header().Set("x-codex-primary-window-minutes", "300")
	w.Header().Set("x-codex-primary-reset-at", strconv.FormatInt(runtimeAccountResetAt, 10))
	runtimeTextModel(w, request, "ACCOUNT_001_MODEL_DONE", fmt.Sprintf("account-001-%d", step))
}

// ACCOUNT-001：登录及通知均来自真实 CLI；磁盘凭据还必须用于真实模型请求。
// 此专项不声称 API-key 支持 ChatGPT 账户配额/usage 的成功读取。
func verifyRuntimeCodexAccount(t *testing.T, ctx context.Context, registry *RuntimeRegistry, connection *ssh.Client, fixture *runtimeCodexAccountFixture, root string) {
	t.Helper()
	codexHome := filepath.Join(root, string(runtimeidentity.Codex), "config")
	claudeHome := filepath.Join(root, string(runtimeidentity.Claude))
	authFile := filepath.Join(codexHome, "auth.json")
	claudeGeneration := registry.entries[runtimeidentity.Claude].Runtime.Generation()
	claudeConfig := runtimeAccountClaudeConfig(t, claudeHome)
	connect := func() (*codex.SocketClient, *protocolTraceTransport, *codex.EventSubscription) {
		client, trace := connectRuntimeSSHWithTrace(t, ctx, connection, runtimeidentity.Codex, codex.SocketClientOptions{})
		events := client.Subscribe(codex.ThreadFilter{})
		t.Cleanup(events.Close)
		return client, trace, events
	}
	client, trace, events := connect()
	runtimeAccountRead(t, ctx, client, false)
	_, err := os.Stat(authFile)
	require.True(t, os.IsNotExist(err), "测试登录前不能有预制 auth.json")
	require.Zero(t, fixture.calls.Load())
	var login struct{ Type string }
	require.NoError(t, client.Call(ctx, "account/login/start", map[string]any{
		"type": "apiKey", "apiKey": runtimeAccountTestKey,
	}, &login))
	require.Equal(t, "apiKey", login.Type)
	runtimeAccountNotifications(t, ctx, events, true)
	runtimeAccountRead(t, ctx, client, true)
	runtimeAccountFile(t, authFile)
	require.Zero(t, fixture.calls.Load(), "登录和读取账户不能请求模型")
	thread := readSessionThread(t, ctx, client, "thread/start", map[string]any{
		"cwd": filepath.Join(root, "project"), "approvalPolicy": "never", "sandbox": "danger-full-access",
	})
	runtimeAccountTurn(t, ctx, client, events, thread.ID, "ACCOUNT_001_BEFORE_RESTART", 17)
	require.Equal(t, int64(1), fixture.calls.Load())

	generation := registry.entries[runtimeidentity.Codex].Runtime.Generation()
	trace.expectClose("runtime-restart")
	require.NoError(t, registry.Restart(runtimeidentity.Codex))
	require.Greater(t, registry.entries[runtimeidentity.Codex].Runtime.Generation(), generation)
	require.Equal(t, claudeGeneration, registry.entries[runtimeidentity.Claude].Runtime.Generation())
	client, trace, events = connect()
	runtimeAccountRead(t, ctx, client, true)
	runtimeAccountFile(t, authFile)
	readSessionThread(t, ctx, client, "thread/resume", map[string]any{"threadId": thread.ID})
	require.Equal(t, int64(1), fixture.calls.Load(), "重启、账户和会话恢复不能额外请求模型")
	runtimeAccountTurn(t, ctx, client, events, thread.ID, "ACCOUNT_001_AFTER_RESTART", 29)
	require.Equal(t, int64(2), fixture.calls.Load())

	var logout map[string]any
	require.NoError(t, client.Call(ctx, "account/logout", nil, &logout))
	require.Empty(t, logout)
	runtimeAccountNotifications(t, ctx, events, false)
	runtimeAccountRead(t, ctx, client, false)
	_, err = os.Stat(authFile)
	require.True(t, os.IsNotExist(err), "logout 必须删除实际 file-store 凭据")
	generation = registry.entries[runtimeidentity.Codex].Runtime.Generation()
	trace.expectClose("runtime-restart")
	require.NoError(t, registry.Restart(runtimeidentity.Codex))
	require.Greater(t, registry.entries[runtimeidentity.Codex].Runtime.Generation(), generation)
	client, _, _ = connect()
	runtimeAccountRead(t, ctx, client, false)
	_, err = os.Stat(authFile)
	require.True(t, os.IsNotExist(err))
	require.Equal(t, int64(2), fixture.calls.Load(), "退出及重启不得请求模型")
	require.Equal(t, claudeGeneration, registry.entries[runtimeidentity.Claude].Runtime.Generation())
	require.Equal(t, claudeConfig, runtimeAccountClaudeConfig(t, claudeHome), "Codex 账户操作不得改写 Claude 配置和凭据")
	_, err = os.Stat(filepath.Join(claudeHome, "config", "auth.json"))
	require.True(t, os.IsNotExist(err), "Codex 登录凭据不得写入 Claude 配置目录")
}

func runtimeAccountRead(t *testing.T, ctx context.Context, client *codex.SocketClient, loggedIn bool) {
	t.Helper()
	var raw json.RawMessage
	require.NoError(t, client.Call(ctx, "account/read", map[string]any{"refreshToken": false}, &raw))
	require.False(t, strings.Contains(string(raw), runtimeAccountTestKey), "公开账户响应不能回显凭据")
	var account struct {
		Account            *struct{ Type string }
		RequiresOpenAIAuth bool `json:"requiresOpenaiAuth"`
	}
	require.NoError(t, json.Unmarshal(raw, &account))
	require.True(t, account.RequiresOpenAIAuth, "必须真实测试需要账户凭据的 provider")
	if loggedIn {
		require.NotNil(t, account.Account)
		require.Equal(t, "apiKey", account.Account.Type)
	} else {
		require.Nil(t, account.Account)
	}
}

func runtimeAccountFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var auth map[string]any
	require.NoError(t, json.Unmarshal(data, &auth))
	require.True(t, auth["OPENAI_API_KEY"] == runtimeAccountTestKey, "原生 auth.json 必须持久化本次虚拟凭据")
}

func runtimeAccountClaudeConfig(t *testing.T, home string) map[string][32]byte {
	t.Helper()
	result := map[string][32]byte{}
	for _, name := range []string{"runtime.env", "config/claude/settings.json", "config/claude/CLAUDE.md"} {
		data, err := os.ReadFile(filepath.Join(home, name))
		require.NoError(t, err)
		result[name] = sha256.Sum256(data)
	}
	return result
}

func runtimeAccountNotifications(t *testing.T, ctx context.Context, events *codex.EventSubscription, loggedIn bool) {
	t.Helper()
	completed, updated := !loggedIn, false
	for !completed || !updated {
		select {
		case <-ctx.Done():
			t.Fatalf("未收到原生账户终态通知：loginCompleted=%t accountUpdated=%t", completed, updated)
		case event, ok := <-events.Events():
			require.True(t, ok, "账户事件连接提前关闭")
			require.False(t, strings.Contains(string(event.Params), runtimeAccountTestKey), "原生账户通知不得回显凭据")
			switch event.Method {
			case "account/login/completed":
				var result struct {
					LoginID *string `json:"loginId"`
					Success bool
					Error   *string
				}
				require.NoError(t, json.Unmarshal(event.Params, &result))
				require.True(t, loggedIn, "退出不能发出登录完成通知")
				require.Nil(t, result.LoginID, "API-key 登录没有 browser loginId")
				require.True(t, result.Success)
				require.Nil(t, result.Error)
				completed = true
			case "account/updated":
				var result struct{ AuthMode, PlanType *string }
				require.NoError(t, json.Unmarshal(event.Params, &result))
				require.Nil(t, result.PlanType, "API-key 账户不能伪造订阅套餐")
				if loggedIn {
					if result.AuthMode == nil {
						continue
					}
					require.Equal(t, "apikey", *result.AuthMode)
				} else {
					require.Nil(t, result.AuthMode)
				}
				updated = true
			}
		}
	}
}

func runtimeAccountTurn(t *testing.T, ctx context.Context, client *codex.SocketClient, events *codex.EventSubscription, threadID, input string, usedPercent float64) {
	t.Helper()
	var started struct{ Turn struct{ ID string } }
	require.NoError(t, client.Call(ctx, "turn/start", map[string]any{
		"threadId": threadID, "input": []map[string]string{{"type": "text", "text": input}},
	}, &started))
	require.NotEmpty(t, started.Turn.ID)
	completed, limits := false, false
	for !completed || !limits {
		select {
		case <-ctx.Done():
			t.Fatalf("账户模型验收缺少真实终态：turnCompleted=%t matchingRateLimits=%t", completed, limits)
		case event, ok := <-events.Events():
			require.True(t, ok)
			require.False(t, strings.Contains(string(event.Params), runtimeAccountTestKey), "公开事件不得回显模型认证头")
			switch event.Method {
			case "account/rateLimits/updated":
				var result struct {
					RateLimits struct {
						LimitID string `json:"limitId"`
						Primary *struct {
							UsedPercent        float64
							WindowDurationMins int64
							ResetsAt           int64
						}
					}
				}
				require.NoError(t, json.Unmarshal(event.Params, &result))
				// 原生也可发送缓存或空窗口；必须匹配本次真实 HTTP 头，不能只看事件出现。
				window := result.RateLimits.Primary
				if result.RateLimits.LimitID != "codex" || window == nil || window.UsedPercent != usedPercent {
					continue
				}
				require.Equal(t, int64(300), window.WindowDurationMins)
				require.Equal(t, runtimeAccountResetAt, window.ResetsAt)
				limits = true
			case "turn/completed":
				var result struct {
					ThreadID string `json:"threadId"`
					Turn     struct{ ID, Status string }
				}
				require.NoError(t, json.Unmarshal(event.Params, &result))
				require.Equal(t, threadID, result.ThreadID)
				require.Equal(t, started.Turn.ID, result.Turn.ID)
				require.Equal(t, "completed", result.Turn.Status)
				completed = true
			}
		}
	}
	var history struct {
		Thread struct {
			Turns []struct {
				ID, Status string
				Items      []struct{ Type, Text string }
			}
		}
	}
	require.NoError(t, client.Call(ctx, "thread/read", map[string]any{"threadId": threadID, "includeTurns": true}, &history))
	found := false
	for _, turn := range history.Thread.Turns {
		if turn.ID != started.Turn.ID {
			continue
		}
		require.Equal(t, "completed", turn.Status)
		for _, item := range turn.Items {
			if item.Type == "agentMessage" && item.Text == "ACCOUNT_001_MODEL_DONE" {
				found = true
			}
		}
	}
	require.True(t, found, "原生模型回复必须进入真实会话历史")
}
