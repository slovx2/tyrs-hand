//go:build integration

package hostworker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestRuntimeClaudeAccountRealSSH(t *testing.T) {
	testRuntimeRegistryRealSSH(t, "claude-account")
}

type runtimeClaudeAccountFixture struct {
	calls    atomic.Int64
	resetsAt int64
}

func (f *runtimeClaudeAccountFixture) model(t *testing.T, w http.ResponseWriter, request *http.Request, body []byte) {
	t.Helper()
	step := f.calls.Add(1)
	require.LessOrEqual(t, step, int64(3), "账户读取和重启不得额外调用模型")
	require.Contains(t, string(body), fmt.Sprintf("ACCOUNT_003_%d", step))
	window, claim, utilization, status := "5h", "five_hour", "0.2", "allowed"
	switch step {
	case 2:
		utilization, status = "0.95", "allowed_warning"
		w.Header().Set("anthropic-ratelimit-unified-5h-surpassed-threshold", "0.9")
	case 3:
		window, claim, utilization = "7d", "seven_day", "0.325"
	}
	w.Header().Set("anthropic-ratelimit-unified-status", status)
	w.Header().Set("anthropic-ratelimit-unified-representative-claim", claim)
	w.Header().Set("anthropic-ratelimit-unified-reset", strconv.FormatInt(f.resetsAt, 10))
	w.Header().Set("anthropic-ratelimit-unified-"+window+"-reset", strconv.FormatInt(f.resetsAt, 10))
	w.Header().Set("anthropic-ratelimit-unified-"+window+"-utilization", utilization)
	runtimeTextModel(w, request, "ACCOUNT_003_DONE", fmt.Sprintf("claude-account-%d", step))
}

type claudeAccountWindow struct {
	UsedPercent, WindowDurationMins int
	ResetsAt                        int64
}

type claudeAccountSnapshot struct {
	LimitID                                 string
	Primary, Secondary                      *claudeAccountWindow
	Credits, PlanType, RateLimitReachedType any
}

type claudeAccountResponse struct {
	RateLimits          claudeAccountSnapshot
	RateLimitsByLimitID map[string]claudeAccountSnapshot
}

func readClaudeAccountLimits(t *testing.T, ctx context.Context, client *codex.SocketClient) claudeAccountSnapshot {
	t.Helper()
	var response claudeAccountResponse
	require.NoError(t, client.Call(ctx, "account/rateLimits/read", nil, &response))
	require.Equal(t, "claude-code", response.RateLimits.LimitID)
	require.Equal(t, response.RateLimits, response.RateLimitsByLimitID["claude-code"])
	require.Nil(t, response.RateLimits.Credits, "不能从原生比例伪造账户余额")
	require.Nil(t, response.RateLimits.PlanType)
	require.Nil(t, response.RateLimits.RateLimitReachedType)
	return response.RateLimits
}

// ACCOUNT-003：真实 HTTP 限额头经 CLI、适配器、Hub 和 SSH 转成账户事件及快照。
// 仅验证同一显式虚拟 OAuth 账户，不声明动态 helper 或其他项目 provider 已支持。
func verifyRuntimeClaudeAccount(t *testing.T, ctx context.Context, registry *RuntimeRegistry,
	connection *ssh.Client, codexClient *codex.SocketClient, fixture *runtimeClaudeAccountFixture, root string,
) {
	t.Helper()
	codexGeneration := registry.entries[runtimeidentity.Codex].Runtime.Generation()
	codexEvents := codexClient.Subscribe(codex.ThreadFilter{})
	defer codexEvents.Close()
	client, trace := connectRuntimeSSHWithTrace(t, ctx, connection, runtimeidentity.Claude, codex.SocketClientOptions{})
	events := client.Subscribe(codex.ThreadFilter{})
	defer events.Close()
	initial := readClaudeAccountLimits(t, ctx, client)
	require.Nil(t, initial.Primary)
	require.Nil(t, initial.Secondary)
	require.Zero(t, fixture.calls.Load())
	var previous claudeAccountSnapshot
	for step := 1; step <= 3; step++ {
		thread := readSessionThread(t, ctx, client, "thread/start", map[string]any{
			"cwd": filepath.Join(root, "project"), "approvalPolicy": "never", "sandbox": "danger-full-access",
		})
		if step > 1 {
			require.Equal(t, previous, readClaudeAccountLimits(t, ctx, client), "新会话必须共享同一真实账户的观测值")
		}
		var turn struct{ Turn struct{ ID string } }
		require.NoError(t, client.Call(ctx, "turn/start", map[string]any{"threadId": thread.ID,
			"input": []map[string]any{{"type": "text", "text": fmt.Sprintf("ACCOUNT_003_%d", step), "text_elements": []any{}}}}, &turn))
		updates := 0
		var notified claudeAccountSnapshot
		completed := false
		for !completed {
			select {
			case <-ctx.Done():
				t.Fatal("真实限额通知或回合完成未送达")
			case event, ok := <-events.Events():
				require.True(t, ok)
				if event.Method == "account/rateLimits/updated" {
					var params struct{ RateLimits claudeAccountSnapshot }
					require.NoError(t, json.Unmarshal(event.Params, &params))
					notified = params.RateLimits
					updates++
				}
				if event.Method == "turn/completed" {
					var params struct {
						ThreadID string
						Turn     struct{ ID, Status string }
					}
					require.NoError(t, json.Unmarshal(event.Params, &params))
					require.Equal(t, thread.ID, params.ThreadID)
					require.Equal(t, turn.Turn.ID, params.Turn.ID)
					require.Equal(t, "completed", params.Turn.Status)
					completed = true
				}
			}
		}
		previous = readClaudeAccountLimits(t, ctx, client)
		t.Logf("ACCOUNT-003 step=%d notifications=%d primary=%v secondary=%v", step, updates, previous.Primary, previous.Secondary)
		require.Equal(t, 1, updates, "每次原生比例变化只能产生一次账户通知")
		require.Equal(t, previous, notified, "SSH 查询必须返回实际通知的完整快照")
		if step <= 2 {
			require.Equal(t, &claudeAccountWindow{UsedPercent: map[int]int{1: 20, 2: 95}[step],
				WindowDurationMins: 300, ResetsAt: fixture.resetsAt}, previous.Primary)
		} else {
			require.Equal(t, &claudeAccountWindow{UsedPercent: 33, WindowDurationMins: 10080,
				ResetsAt: fixture.resetsAt}, previous.Secondary)
		}
		require.Equal(t, int64(step), fixture.calls.Load())
	}
	trace.expectClose("runtime-restart")
	require.NoError(t, registry.Restart(runtimeidentity.Claude))
	client, _ = connectRuntimeSSHWithTrace(t, ctx, connection, runtimeidentity.Claude, codex.SocketClientOptions{})
	restarted := readClaudeAccountLimits(t, ctx, client)
	require.Nil(t, restarted.Primary, "重启不能把旧观测假装成当前账户额度")
	require.Nil(t, restarted.Secondary)
	require.Equal(t, int64(3), fixture.calls.Load())
	require.Equal(t, codexGeneration, registry.entries[runtimeidentity.Codex].Runtime.Generation())
	for {
		select {
		case event := <-codexEvents.Events():
			require.NotEqual(t, "account/rateLimits/updated", event.Method, "Claude 限额不得泄漏给 Codex 入口")
		default:
			return
		}
	}
}
