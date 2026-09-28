//go:build integration

package hostworker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

// CAPABILITY-003：外围能力经真实 SSH 与 Hub 到达各自运行时。Codex 原样透传至固定 CLI 并由原生产生响应；
// Claude 对客户端依赖的发现类查询返回真实状态（无连接器、Windows 沙箱未配置、未检测到外部代理配置）。
// 响应均经 schema 校验，这些调用不创建回合，不能请求模型。
func verifyRuntimeCapabilitySurface(t *testing.T, ctx context.Context, connections map[runtimeidentity.Engine]*ssh.Client) {
	t.Helper()
	verifyRuntimeCodexSurface(t, ctx, connections[runtimeidentity.Codex])
	client := connectRuntimeSSH(t, ctx, connections[runtimeidentity.Claude], runtimeidentity.Claude)
	var apps struct {
		Data       []json.RawMessage
		NextCursor *string
	}
	require.NoError(t, client.Call(ctx, "app/list", map[string]any{}, &apps))
	require.Empty(t, apps.Data, "Claude 没有 ChatGPT 连接器")
	require.Nil(t, apps.NextCursor)
	var readiness struct{ Status string }
	require.NoError(t, client.Call(ctx, "windowsSandbox/readiness", nil, &readiness))
	require.Equal(t, "notConfigured", readiness.Status)
	var setup struct{ Started bool }
	require.NoError(t, client.Call(ctx, "windowsSandbox/setupStart", map[string]any{"mode": "elevated"}, &setup))
	require.False(t, setup.Started, "Linux Worker 不能声称已启动 Windows 沙箱")
	var detected struct{ Items []json.RawMessage }
	require.NoError(t, client.Call(ctx, "externalAgentConfig/detect", map[string]any{}, &detected))
	require.Empty(t, detected.Items)
}

func verifyRuntimeCodexSurface(t *testing.T, ctx context.Context, connection *ssh.Client) {
	t.Helper()
	client := connectRuntimeSSH(t, ctx, connection, runtimeidentity.Codex)
	for _, call := range []struct {
		method string
		params any
		keys   []string
	}{
		{"account/bedrock/discover", map[string]any{}, []string{"profiles", "environmentCredentials"}},
		{"account/gatewayOAuth/read", nil, []string{"providerId", "status"}},
		{"account/gatewayOAuth/cancel", nil, nil},
		{"app/installed", map[string]any{}, []string{"apps"}},
		{"app/list", map[string]any{}, []string{"data"}},
		{"app/read", map[string]any{"appIds": []string{}}, []string{"apps", "missingAppIds"}},
		{"memory/status", map[string]any{}, []string{"v2ConsolidatedThreads", "v2Ready"}},
		{"plugin/reconcile", map[string]any{}, []string{"changedPlugins"}},
		{"rollout/compress", nil, nil},
		{"userVerification/status", map[string]any{}, []string{"unavailableReason"}},
		{"userVerification/cancel", map[string]any{"requestId": "capability-003"}, nil},
		{"windowsSandbox/readiness", nil, []string{"status"}},
		{"windowsSandbox/setupStart", map[string]any{"mode": "elevated"}, []string{"started"}},
	} {
		var result map[string]json.RawMessage
		require.NoError(t, client.Call(ctx, call.method, call.params, &result), "%s 必须由原生 CLI 成功响应", call.method)
		for _, key := range call.keys {
			require.Contains(t, result, key, "%s 响应缺少原生字段 %s", call.method, key)
		}
	}
	// 本地 AWS profile 可被原生发现，Bedrock 配置在本地完成而不访问网络。
	var bedrock struct {
		Profiles []struct{ Name, Region string }
	}
	require.NoError(t, client.Call(ctx, "account/bedrock/discover", map[string]any{}, &bedrock))
	require.Contains(t, bedrock.Profiles, struct{ Name, Region string }{"default", "us-east-1"})
	require.NoError(t, client.Call(ctx, "account/bedrock/setup",
		map[string]any{"type": "profile", "profile": "default", "region": "us-east-1"}, nil))
	// 发起后立即取消的登录必须以失败完成通知收尾，不访问授权服务。
	events := client.Subscribe(codex.ThreadFilter{})
	defer events.Close()
	var login struct{ Type, LoginID string }
	require.NoError(t, client.Call(ctx, "account/login/start", map[string]any{"type": "chatgpt"}, &login))
	require.NotEmpty(t, login.LoginID)
	var canceled struct{ Status string }
	require.NoError(t, client.Call(ctx, "account/login/cancel", map[string]any{"loginId": login.LoginID}, &canceled))
	require.Equal(t, "canceled", canceled.Status)
	waitCodexSurfaceNotification(t, ctx, events, "account/login/completed", func(params json.RawMessage) bool {
		var completed struct {
			LoginID *string
			Success bool
		}
		return json.Unmarshal(params, &completed) == nil && !completed.Success &&
			completed.LoginID != nil && *completed.LoginID == login.LoginID
	})
	// 原生会话在最后一个订阅退出、卸载延迟到期后关闭。
	thread := readSessionThread(t, ctx, client, "thread/start", map[string]any{"ephemeral": true})
	require.NoError(t, client.Call(ctx, "thread/unsubscribe", map[string]any{"threadId": thread.ID}, nil))
	waitCodexSurfaceNotification(t, ctx, events, "thread/closed", func(params json.RawMessage) bool {
		var closed struct{ ThreadID string }
		return json.Unmarshal(params, &closed) == nil && closed.ThreadID == thread.ID
	})
	// 非 Windows Worker 不能伪造已就绪的 Windows 沙箱。
	var readiness struct{ Status string }
	require.NoError(t, client.Call(ctx, "windowsSandbox/readiness", nil, &readiness))
	require.NotEqual(t, "ready", readiness.Status, "Linux Worker 不能声称 Windows 沙箱就绪")
}

func waitCodexSurfaceNotification(t *testing.T, ctx context.Context, events *codex.EventSubscription, method string,
	match func(json.RawMessage) bool,
) {
	t.Helper()
	timeout := time.After(15 * time.Second)
	for {
		select {
		case event, ok := <-events.Events():
			require.True(t, ok, "等待 %s 时事件流关闭", method)
			if event.Method == method && match(event.Params) {
				return
			}
		case <-timeout:
			t.Fatalf("未收到原生 %s", method)
		case <-ctx.Done():
			t.Fatalf("等待 %s 超时", method)
		}
	}
}
