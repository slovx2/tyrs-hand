//go:build integration

package hostworker

import (
	"context"
	"crypto/sha256"
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
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestRuntimeCodexMcpOAuthRealSSH(t *testing.T) { testRuntimeRegistryRealSSH(t, "codex-mcp-oauth") }

func TestRuntimeCodexMcpOAuthHeadersRealSSH(t *testing.T) {
	testRuntimeRegistryRealSSH(t, "codex-mcp-oauth")
}

type runtimeCodexOAuthFixture struct {
	root    string
	calls   atomic.Int64
	outputs sync.Map
}

func newRuntimeCodexOAuthFixture(root string) *runtimeCodexOAuthFixture {
	return &runtimeCodexOAuthFixture{root: root}
}

func (f *runtimeCodexOAuthFixture) model(t *testing.T, w http.ResponseWriter, request *http.Request, body []byte) {
	t.Helper()
	require.Equal(t, "/v1/responses", request.URL.Path)
	require.LessOrEqual(t, f.calls.Add(1), int64(4), "OAuth管理、配置和重启不能请求模型")
	var parsed struct {
		Tools []struct {
			Type, Name string
			Tools      []struct{ Name string }
		}
		Input []struct {
			Type, Role      string
			CallID          string `json:"call_id"`
			Content, Output json.RawMessage
		}
	}
	require.NoError(t, json.Unmarshal(body, &parsed))
	stage := ""
	for _, input := range parsed.Input {
		if input.Role != "user" {
			continue
		}
		for _, candidate := range []string{"before", "after"} {
			if strings.Contains(string(input.Content), "CODEX_OAUTH_"+candidate) {
				stage = candidate
			}
		}
	}
	require.NotEmpty(t, stage)
	id := "codex-oauth-" + stage
	for _, input := range parsed.Input {
		if input.Type != "function_call_output" || input.CallID != id {
			continue
		}
		var output string
		require.NoError(t, json.Unmarshal(input.Output, &output))
		require.Contains(t, output, "OAUTH_REAL_EFFECT", "真正授权的MCP工具结果必须回模")
		_, repeated := f.outputs.LoadOrStore(stage, true)
		require.False(t, repeated)
		runtimeTextModel(w, request, "CODEX_OAUTH_DONE_"+stage, id+"-done")
		return
	}
	declared := false
	for _, namespace := range parsed.Tools {
		if namespace.Type != "namespace" || namespace.Name != "mcp__secure" {
			continue
		}
		for _, tool := range namespace.Tools {
			declared = declared || tool.Name == "oauth_write"
		}
	}
	require.True(t, declared, "真实Codex模型目录必须包含授权后的MCP工具")
	w.Header().Set("Content-Type", "text/event-stream")
	for _, value := range []map[string]any{
		{"type": "response.created", "response": map[string]any{"id": id}},
		{"type": "response.output_item.done", "item": map[string]any{"type": "function_call", "namespace": "mcp__secure", "name": "oauth_write", "call_id": id, "arguments": "{}"}},
		{"type": "response.completed", "response": map[string]any{"id": id, "usage": map[string]int{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15}}},
	} {
		encoded, err := json.Marshal(value)
		require.NoError(t, err)
		_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", value["type"], encoded)
		require.NoError(t, err)
	}
}

func runtimeCodexOAuthServer(t *testing.T, ctx context.Context, root, effect string) (string, func()) {
	t.Helper()
	adapter := filepath.Dir(filepath.Dir(os.Getenv("TYRS_HAND_TEST_CLAUDE_BIN")))
	report := filepath.Join(root, "codex-oauth-report.json")
	evidenceReport := report
	requestReport := filepath.Join(root, "codex-oauth-origins.txt")
	if directory := os.Getenv("PROTOCOL_ARTIFACT_DIR"); directory != "" {
		evidenceReport = filepath.Join(directory, "codex-oauth-effects.txt")
		requestReport = filepath.Join(directory, "codex-oauth-origins.txt")
	}
	code := fmt.Sprintf(`import {OAuthMcpFixture} from %q;
import {writeFile} from 'node:fs/promises';
import {writeFileSync} from 'node:fs';
import assert from 'node:assert/strict';
import {StreamableHTTPServerTransport} from %q;
const f=new OAuthMcpFixture(%q);
const requests=[];
for(const [origin,server] of [['resource',f.mcpServer],['authorization',f.authServer]])server.prependListener('request',(req,_res)=>{
 requests.push({role:origin,origin:new URL(origin==='resource'?f.url:f.issuer).origin,path:new URL(req.url,'http://fixture').pathname,resourceHeaderPresent:req.headers['x-resource-only']!==undefined,resourceHeaderMatchesFixture:req.headers['x-resource-only']==='resource-test-secret',authorizationHeaderPresent:req.headers.authorization!==undefined});
 writeFileSync(%q,JSON.stringify(requests));
});
// 独立Codex头隔离夹具允许资源origin接收自己的业务头，跨origin授权服务器仍用原始严格断言。
if(%t){const original=f.mcp.bind(f);f.mcp=async(req,res)=>{
 if(new URL(req.url,f.url).pathname.startsWith('/.well-known/oauth-protected-resource')){
  res.writeHead(200,{'Content-Type':'application/json'}).end(JSON.stringify({resource:f.url,authorization_servers:[f.issuer],scopes_supported:['fixture:write']}));return;
 }
 return original(req,res);
};}else{const original=f.mcp.bind(f);f.mcp=async(req,res)=>{
 // 纯OAuth夹具不要求额外业务头；仍严格验证真正收到的Bearer，绝不改写请求头。
 if(req.headers.authorization!=='Bearer '+f.token||new URL(req.url,f.url).pathname!=='/mcp')return original(req,res);
 f.bearerRequests++;f.authorizedRequests++;assert.equal(req.headers['x-resource-only'],undefined);
 if(req.method==='GET'){res.writeHead(405).end();return;}
 const mcp=f.createMcp(),transport=new StreamableHTTPServerTransport({enableJsonResponse:true});
 res.on('close',()=>{void mcp.close()});await mcp.connect(transport);await transport.handleRequest(req,res);
};}
const summary=()=>JSON.stringify({errors:f.errors,exchanges:f.exchanges,effects:f.effects,authorizedRequests:f.authorizedRequests,refreshes:f.refreshes});
for(const server of [f.authServer,f.mcpServer])server.on('request',(_req,res)=>res.on('finish',()=>writeFileSync(%q,summary())));
console.log(JSON.stringify({url:await f.start()}));
process.on('SIGTERM',async()=>{await f.close();await writeFile(%q,summary());process.exit(0)});`, filepath.Join(adapter, "dist", "test", "fixtures", "mcp-oauth.mjs"), filepath.Join(adapter, "node_modules", "@modelcontextprotocol", "sdk", "dist", "esm", "server", "streamableHttp.js"), effect, requestReport, t.Name() == "TestRuntimeCodexMcpOAuthHeadersRealSSH", evidenceReport, report)
	command := exec.CommandContext(ctx, "node", "--input-type=module", "-e", code)
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root}
	stdout, err := command.StdoutPipe()
	require.NoError(t, err)
	command.Stderr = io.Discard
	require.NoError(t, command.Start())
	var once sync.Once
	stop := func() {
		once.Do(func() {
			_ = command.Process.Signal(syscall.SIGTERM)
			if err := command.Wait(); ctx.Err() == nil {
				require.NoError(t, err)
			}
			if directory := os.Getenv("PROTOCOL_ARTIFACT_DIR"); directory != "" {
				if data, err := os.ReadFile(report); err == nil {
					require.NoError(t, os.WriteFile(filepath.Join(directory, "codex-oauth-effects.txt"), data, 0o600))
				}
			}
		})
	}
	t.Cleanup(stop)
	var endpoint struct{ URL string }
	require.NoError(t, json.NewDecoder(stdout).Decode(&endpoint))
	return endpoint.URL, func() {
		stop()
		data, err := os.ReadFile(report)
		require.NoError(t, err)
		var result struct {
			Errors                                            []string
			Exchanges, Effects, AuthorizedRequests, Refreshes int
		}
		require.NoError(t, json.Unmarshal(data, &result))
		require.Empty(t, result.Errors, "真实OAuth服务的PKCE/resource/头隔离检查不得失败")
		require.Equal(t, 1, result.Exchanges)
		require.Equal(t, 2, result.Effects)
		require.GreaterOrEqual(t, result.AuthorizedRequests, 2)
		require.Zero(t, result.Refreshes, "五分钟有效token不得为绕过持久化问题而反复换取")
		if directory := os.Getenv("PROTOCOL_ARTIFACT_DIR"); directory != "" {
			require.NoError(t, os.WriteFile(filepath.Join(directory, "codex-oauth-effects.txt"), data, 0o600))
		}
	}
}

// MCP018：真实Codex原生OAuth经所属SSH完成，并在重启后再次执行真实授权工具。
func verifyRuntimeCodexOAuth(t *testing.T, ctx context.Context, registry *RuntimeRegistry, connections map[runtimeidentity.Engine]*ssh.Client, fixture *runtimeCodexOAuthFixture, root string) {
	t.Helper()
	claudeGeneration := registry.entries[runtimeidentity.Claude].Runtime.Generation()
	effect := filepath.Join(root, "project", "codex-oauth-effect.txt")
	endpoint, check := runtimeCodexOAuthServer(t, ctx, root, effect)
	client, trace := connectRuntimeSSHWithTrace(t, ctx, connections[runtimeidentity.Codex], runtimeidentity.Codex, codex.SocketClientOptions{})
	require.NoError(t, client.Call(ctx, "config/value/write", map[string]any{"keyPath": "mcp_oauth_credentials_store", "value": "file", "mergeStrategy": "replace"}, nil))
	secure := map[string]any{"url": endpoint, "oauth_resource": endpoint}
	if t.Name() == "TestRuntimeCodexMcpOAuthHeadersRealSSH" {
		secure["http_headers"] = map[string]string{"X-Resource-Only": "resource-test-secret"}
	}
	writeServer := func() {
		require.NoError(t, client.Call(ctx, "config/value/write", map[string]any{"keyPath": "mcp_servers", "mergeStrategy": "replace", "value": map[string]any{"secure": secure}}, nil))
	}
	writeServer()
	events := client.Subscribe(codex.ThreadFilter{})
	defer events.Close()
	var login struct{ AuthorizationURL string }
	require.NoError(t, client.Call(ctx, "mcpServer/oauth/login", map[string]any{"name": "secure", "timeoutSecs": 30, "scopes": []string{"fixture:write"}}, &login))
	runtimeCodexOAuthCallback(t, ctx, connections, login.AuthorizationURL)
	for {
		select {
		case <-ctx.Done():
			t.Fatal("Codex原生OAuth完成事件未到达SSH")
		case event, ok := <-events.Events():
			require.True(t, ok)
			if event.Method != "mcpServer/oauthLogin/completed" {
				continue
			}
			var completed struct {
				Name    string
				Success bool
				Error   *string
			}
			require.NoError(t, json.Unmarshal(event.Params, &completed))
			require.Equal(t, "secure", completed.Name)
			require.True(t, completed.Success)
			require.Nil(t, completed.Error)
			goto authorized
		}
	}
authorized:
	require.Zero(t, fixture.calls.Load())
	// 本正向始终不配置业务头；跨origin业务凭据隔离由独立MCP019严格验收。
	credentialsPath := filepath.Join(root, string(runtimeidentity.Codex), "config", ".credentials.json")
	credentials, err := os.ReadFile(credentialsPath)
	require.NoError(t, err)
	require.True(t, json.Valid(credentials), "OAuth凭据必须真实持久化为合法文件")
	credentialDigest := sha256.Sum256(credentials)
	info, err := os.Stat(credentialsPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	_, err = os.Stat(filepath.Join(root, string(runtimeidentity.Claude), "config", ".credentials.json"))
	require.True(t, os.IsNotExist(err), "Codex OAuth凭据不能写入Claude配置目录")
	runtimeCodexOAuthTurn(t, ctx, client, fixture, "before")
	runtimeCodexMcpFile(t, effect, "OAUTH_REAL_EFFECT\n")
	trace.expectClose("runtime-restart")
	generation := registry.entries[runtimeidentity.Codex].Runtime.Generation()
	require.NoError(t, registry.Restart(runtimeidentity.Codex))
	require.Greater(t, registry.entries[runtimeidentity.Codex].Runtime.Generation(), generation)
	require.Equal(t, claudeGeneration, registry.entries[runtimeidentity.Claude].Runtime.Generation())
	resumed, _ := connectRuntimeSSHWithTrace(t, ctx, connections[runtimeidentity.Codex], runtimeidentity.Codex, codex.SocketClientOptions{})
	var status struct {
		Data []struct{ Name, AuthStatus string }
	}
	require.NoError(t, resumed.Call(ctx, "mcpServerStatus/list", map[string]any{}, &status))
	require.Len(t, status.Data, 1)
	require.Equal(t, "secure", status.Data[0].Name)
	require.Equal(t, "oAuth", status.Data[0].AuthStatus)
	runtimeCodexOAuthTurn(t, ctx, resumed, fixture, "after")
	runtimeCodexMcpFile(t, effect, "OAUTH_REAL_EFFECT\nOAUTH_REAL_EFFECT\n")
	credentialsAfter, err := os.ReadFile(credentialsPath)
	require.NoError(t, err)
	require.Equal(t, credentialDigest, sha256.Sum256(credentialsAfter), "重启后必须复用已持久化凭据")
	require.Equal(t, int64(4), fixture.calls.Load())
	check()
}

func runtimeCodexOAuthCallback(t *testing.T, ctx context.Context, connections map[runtimeidentity.Engine]*ssh.Client, authorizationURL string) {
	t.Helper()
	authorization, err := url.Parse(authorizationURL)
	require.NoError(t, err)
	callback, err := url.Parse(authorization.Query().Get("redirect_uri"))
	require.NoError(t, err)
	require.NotEmpty(t, callback.Host)
	wrongEngine, err := connections[runtimeidentity.Claude].Dial("tcp", callback.Host)
	if wrongEngine != nil {
		_ = wrongEngine.Close()
	}
	require.Error(t, err, "Claude入口不能访问Codex的OAuth回调")
	otherPort, err := connections[runtimeidentity.Codex].Dial("tcp", "127.0.0.1:22")
	if otherPort != nil {
		_ = otherPort.Close()
	}
	require.Error(t, err, "OAuth不能放开任意SSH端口转发")
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, DialContext: func(_ context.Context, network, address string) (net.Conn, error) {
		return connections[runtimeidentity.Codex].Dial(network, address)
	}}
	defer transport.CloseIdleConnections()
	browser := &http.Client{Transport: transport}
	get := func(client *http.Client, address string) *http.Response {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
		require.NoError(t, err)
		response, err := client.Do(request)
		require.NoError(t, err)
		return response
	}
	invalid := *callback
	values := url.Values{"state": []string{"wrong-state"}, "code": []string{"wrong-code"}}
	invalid.RawQuery = values.Encode()
	response := get(browser, invalid.String())
	require.Equal(t, http.StatusForbidden, response.StatusCode)
	_ = response.Body.Close()
	invalid.Path = "/unrelated-service"
	values.Set("state", authorization.Query().Get("state"))
	invalid.RawQuery = values.Encode()
	response = get(browser, invalid.String())
	require.Equal(t, http.StatusForbidden, response.StatusCode)
	_ = response.Body.Close()
	// 授权服务器是本地Mock；只有最终callback经过真实Codex SSH转发。
	authorize := &http.Client{Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	defer authorize.CloseIdleConnections()
	response = get(authorize, authorizationURL)
	require.Equal(t, http.StatusFound, response.StatusCode)
	returned := response.Header.Get("Location")
	_ = response.Body.Close()
	response = get(browser, returned)
	require.Equal(t, http.StatusOK, response.StatusCode)
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	replay, err := connections[runtimeidentity.Codex].Dial("tcp", callback.Host)
	if replay != nil {
		_ = replay.Close()
	}
	require.Error(t, err, "成功回调消费后必须撤回转发许可")
}

func runtimeCodexOAuthTurn(t *testing.T, ctx context.Context, client *codex.SocketClient, fixture *runtimeCodexOAuthFixture, stage string) {
	t.Helper()
	thread := readSessionThread(t, ctx, client, "thread/start", map[string]any{"cwd": filepath.Join(fixture.root, "project"), "approvalPolicy": "never", "sandbox": "danger-full-access"})
	events := client.Subscribe(codex.ThreadFilter{ThreadID: thread.ID})
	defer events.Close()
	var started struct{ Turn struct{ ID string } }
	require.NoError(t, client.Call(ctx, "turn/start", map[string]any{"threadId": thread.ID, "input": []map[string]string{{"type": "text", "text": "CODEX_OAUTH_" + stage}}}, &started))
	toolCompleted := false
	for {
		select {
		case <-ctx.Done():
			t.Fatal("真实OAuth MCP工具Turn未完成")
		case event, ok := <-events.Events():
			require.True(t, ok)
			if event.Method != "item/completed" && event.Method != "turn/completed" {
				continue
			}
			var params struct {
				ThreadID, TurnID string
				Item             struct {
					Type, Server, Tool, Status string
					Error                      any
					Result                     struct{ Content []struct{ Text string } }
				}
				Turn struct {
					ID, Status string
					Error      any
				}
			}
			require.NoError(t, json.Unmarshal(event.Params, &params))
			require.Equal(t, thread.ID, params.ThreadID)
			if event.Method == "turn/completed" {
				require.Equal(t, started.Turn.ID, params.Turn.ID)
				require.Equal(t, "completed", params.Turn.Status)
				require.Nil(t, params.Turn.Error)
				require.True(t, toolCompleted)
				_, returned := fixture.outputs.Load(stage)
				require.True(t, returned)
				return
			}
			if params.Item.Type != "mcpToolCall" {
				continue
			}
			require.False(t, toolCompleted)
			require.Equal(t, started.Turn.ID, params.TurnID)
			require.Equal(t, "secure", params.Item.Server)
			require.Equal(t, "oauth_write", params.Item.Tool)
			require.Equal(t, "completed", params.Item.Status)
			require.Nil(t, params.Item.Error)
			require.Len(t, params.Item.Result.Content, 1)
			require.Equal(t, "OAUTH_REAL_EFFECT", params.Item.Result.Content[0].Text)
			toolCompleted = true
		}
	}
}
