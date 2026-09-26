//go:build integration

package hostworker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestRuntimeMcpOAuthRealSSH(t *testing.T) { testRuntimeRegistryRealSSH(t, "mcp-oauth") }

type runtimeOAuthFixture struct{ calls atomic.Int64 }

func (f *runtimeOAuthFixture) model(t *testing.T, w http.ResponseWriter, request *http.Request, body []byte) {
	step := f.calls.Add(1)
	if step == 2 {
		require.True(t, strings.Contains(string(body), "OAUTH_REAL_EFFECT"), "真实 MCP 返回必须进入模型续写")
		runtimeTextModel(w, request, "OAUTH_SSH_SDK_DONE", "oauth-ssh-done")
		return
	}
	if step != 1 {
		t.Error("OAuth 流程产生非预期模型调用")
		http.Error(w, "unexpected model", 400)
		return
	}
	require.True(t, strings.Contains(string(body), "mcp__secure__oauth_write"), "授权后的 MCP 工具必须进入模型目录")
	w.Header().Set("Content-Type", "text/event-stream")
	event := func(kind string, value map[string]any) {
		value["type"] = kind
		encoded, err := json.Marshal(value)
		require.NoError(t, err)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, encoded)
	}
	event("message_start", map[string]any{"message": map[string]any{"id": "msg_oauth_ssh", "type": "message", "role": "assistant", "model": "claude-config-model", "content": []any{}, "stop_reason": nil, "usage": map[string]int{"input_tokens": 10, "output_tokens": 1}}})
	event("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "tool_use", "id": "toolu_oauth_ssh", "name": "mcp__secure__oauth_write", "input": map[string]any{}}})
	event("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": "{}"}})
	event("content_block_stop", map[string]any{"index": 0})
	event("message_delta", map[string]any{"delta": map[string]any{"stop_reason": "tool_use"}, "usage": map[string]int{"output_tokens": 5}})
	event("message_stop", map[string]any{})
}

// MCP-013：客户端 HTTP 回调只能通过所属引擎的真实 SSH，随后真实 SDK 执行 MCP 写文件。
func verifyRuntimeMcpOAuth(t *testing.T, ctx context.Context, registry *RuntimeRegistry, connections map[runtimeidentity.Engine]*ssh.Client, clients map[runtimeidentity.Engine]*codex.SocketClient, signer ssh.Signer) {
	t.Helper()
	root := registry.entries[runtimeidentity.Claude].Runtime.WorkspaceRoot()
	effect := filepath.Join(root, "oauth-ssh-effect.txt")
	adapter := filepath.Dir(filepath.Dir(os.Getenv("TYRS_HAND_TEST_CLAUDE_BIN")))
	fixture := filepath.Join(adapter, "dist", "test", "fixtures", "mcp-oauth.mjs")
	code := fmt.Sprintf("import { OAuthMcpFixture } from %q; const fixture=new OAuthMcpFixture(%q); const url=await fixture.start(); console.log(JSON.stringify({url})); process.on('SIGTERM',async()=>{await fixture.close();process.exit(0)});", fixture, effect)
	command := exec.CommandContext(ctx, "node", "--input-type=module", "-e", code)
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root}
	output, err := command.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, command.Start())
	t.Cleanup(func() { _ = command.Process.Signal(syscall.SIGTERM); _ = command.Wait() })
	var endpoint struct{ URL string }
	require.NoError(t, json.NewDecoder(output).Decode(&endpoint))
	client := clients[runtimeidentity.Claude]
	var result any
	require.NoError(t, client.Call(ctx, "config/value/write", map[string]any{"keyPath": "mcp_servers", "mergeStrategy": "replace", "value": map[string]any{"secure": map[string]any{"url": endpoint.URL, "http_headers": map[string]string{"X-Resource-Only": "resource-test-secret"}}}}, &result))
	var login struct{ AuthorizationURL string }
	subscription := client.Subscribe(codex.ThreadFilter{})
	defer subscription.Close()
	require.NoError(t, client.Call(ctx, "mcpServer/oauth/login", map[string]any{"name": "secure", "timeoutSecs": 30, "scopes": []string{"fixture:write"}}, &login))
	authorization, err := url.Parse(login.AuthorizationURL)
	require.NoError(t, err)
	callback, err := url.Parse(authorization.Query().Get("redirect_uri"))
	require.NoError(t, err)
	wrongEngine, err := connections[runtimeidentity.Codex].Dial("tcp", callback.Host)
	if wrongEngine != nil {
		_ = wrongEngine.Close()
	}
	require.Error(t, err, "另一引擎不能转发 Claude OAuth 回调")
	unauthorized, err := connections[runtimeidentity.Claude].Dial("tcp", "127.0.0.1:22")
	if unauthorized != nil {
		_ = unauthorized.Close()
	}
	require.Error(t, err, "OAuth 不能开启通用端口转发")
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, DialContext: func(_ context.Context, network, address string) (net.Conn, error) {
		return connections[runtimeidentity.Claude].Dial(network, address)
	}}
	defer transport.CloseIdleConnections()
	browser := &http.Client{Transport: transport}
	invalid := *callback
	values := url.Values{"state": []string{"wrong-state"}, "code": []string{"wrong-code"}}
	invalid.RawQuery = values.Encode()
	response, err := browser.Get(invalid.String())
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, response.StatusCode)
	_ = response.Body.Close()
	invalid.Path = "/unrelated-service"
	values.Set("state", authorization.Query().Get("state"))
	invalid.RawQuery = values.Encode()
	response, err = browser.Get(invalid.String())
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, response.StatusCode)
	_ = response.Body.Close()
	authorize := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	response, err = authorize.Get(login.AuthorizationURL)
	require.NoError(t, err)
	require.Equal(t, http.StatusFound, response.StatusCode)
	returned := response.Header.Get("Location")
	_ = response.Body.Close()
	response, err = browser.Get(returned)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	for {
		select {
		case event := <-subscription.Events():
			if event.Method != "mcpServer/oauthLogin/completed" {
				continue
			}
			var completed struct {
				Name    string
				Success bool
			}
			require.NoError(t, json.Unmarshal(event.Params, &completed))
			require.True(t, completed.Success)
			require.Equal(t, "secure", completed.Name)
			goto authorized
		case <-ctx.Done():
			t.Fatal("真实 OAuth 完成通知未到达 SSH 客户端")
		}
	}
authorized:
	replay, err := connections[runtimeidentity.Claude].Dial("tcp", callback.Host)
	if replay != nil {
		_ = replay.Close()
	}
	require.Error(t, err, "单次回调完成后必须收回转发许可")
	thread := readSessionThread(t, ctx, client, "thread/start", map[string]any{"cwd": root, "approvalPolicy": "never", "sandbox": "danger-full-access"})
	var started struct{ Turn struct{ ID string } }
	require.NoError(t, client.Call(ctx, "turn/start", map[string]any{"threadId": thread.ID, "input": []map[string]any{{"type": "text", "text": "Use the OAuth MCP tool once."}}}, &started))
	waitSessionTurn(t, ctx, client, thread.ID, started.Turn.ID)
	contents, err := os.ReadFile(effect)
	require.NoError(t, err)
	require.Equal(t, "OAUTH_REAL_EFFECT\n", string(contents))
	require.NoError(t, registry.Restart(runtimeidentity.Claude))
	resumed := connectRuntimeSSH(t, ctx, connections[runtimeidentity.Claude], runtimeidentity.Claude)
	var status struct{ Data []struct{ AuthStatus string } }
	require.NoError(t, resumed.Call(ctx, "mcpServerStatus/list", map[string]any{}, &status))
	require.Len(t, status.Data, 1)
	require.Equal(t, "oAuth", status.Data[0].AuthStatus)
	if directory := os.Getenv("PROTOCOL_ARTIFACT_DIR"); directory != "" {
		evidence, err := json.MarshalIndent(map[string]any{"case": "MCP-013", "engine": "claude-code", "wrongEngineRejected": true, "unrelatedPortRejected": true, "wrongStateStatus": 403, "wrongPathStatus": 403, "authorizedStatus": 200, "replayRejected": true, "fileContents": string(contents), "fileWrites": strings.Count(string(contents), "\n"), "restartAuthStatus": status.Data[0].AuthStatus}, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(directory, "oauth-ssh-effects.json"), evidence, 0o600))
	}
	verifyRuntimeOAuthCancellation(t, ctx, registry, signer, fixture)
}
