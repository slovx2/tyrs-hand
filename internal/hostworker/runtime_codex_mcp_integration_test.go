//go:build integration

package hostworker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

func TestRuntimeCodexMcpRealSSH(t *testing.T) {
	testRuntimeRegistryRealSSH(t, "codex-mcp")
}

// 真实分页回归保持严格要求；固定 CLI 尚漏掉第二页工具时，此用例必须失败。
func TestRuntimeCodexMcpPaginationRealSSH(t *testing.T) {
	testRuntimeRegistryRealSSH(t, "codex-mcp")
}

type runtimeCodexMcpFixture struct {
	root    string
	calls   atomic.Int64
	outputs sync.Map
}

func newRuntimeCodexMcpFixture(root string) *runtimeCodexMcpFixture {
	return &runtimeCodexMcpFixture{root: root}
}

type runtimeCodexMcpScenario struct{ name, server, tool, action string }

var runtimeCodexMcpScenarios = []runtimeCodexMcpScenario{
	{"http-write", "httpfixture", "touch_fixture", ""},
	{"stdio-write", "manager", "append", ""},
	{"stdio-error", "manager", "fail", ""},
	{"form-accept", "formfixture", "confirm_fixture", "accept"},
	{"form-decline", "formfixture", "confirm_fixture", "decline"},
	{"form-cancel", "formfixture", "confirm_fixture", "cancel"},
	{"url-accept", "urlfixture", "confirm_fixture", "accept"},
	{"url-cancel", "urlfixture", "confirm_fixture", "cancel"},
}

func (f *runtimeCodexMcpFixture) model(t *testing.T, w http.ResponseWriter, request *http.Request, body []byte) {
	t.Helper()
	require.Equal(t, "/v1/responses", request.URL.Path)
	require.LessOrEqual(t, f.calls.Add(1), int64(16), "MCP 管理不得调用模型，工具结果不能重复回模")
	var parsed struct {
		Tools []struct {
			Type, Name string
			Tools      []struct{ Type, Name string }
		}
		Input []struct {
			Role, Type string
			CallID     string `json:"call_id"`
			Content    json.RawMessage
			Output     json.RawMessage
		}
	}
	require.NoError(t, json.Unmarshal(body, &parsed))
	var scenario runtimeCodexMcpScenario
	for _, input := range parsed.Input {
		if input.Role == "user" {
			for _, candidate := range runtimeCodexMcpScenarios {
				if strings.Contains(string(input.Content), "CODEX_MCP_"+candidate.name) {
					scenario = candidate
				}
			}
		}
	}
	require.NotEmpty(t, scenario.name, "只能接受本专项登记的模型请求")
	id := "codex-mcp-" + scenario.name
	for _, input := range parsed.Input {
		if input.Type != "function_call_output" || input.CallID != id {
			continue
		}
		var output string
		require.NoError(t, json.Unmarshal(input.Output, &output))
		_, repeated := f.outputs.LoadOrStore(scenario.name, true)
		require.False(t, repeated, "一个真实工具结果只能回模一次")
		switch scenario.name {
		case "http-write":
			require.Contains(t, output, "MCP_FILE_WRITTEN")
		case "stdio-write":
			require.Contains(t, output, "MCP_MARKER_AFTER_RELOAD")
		case "stdio-error":
			require.Contains(t, output, "FIXTURE_TOOL_ERROR")
		default:
			require.Contains(t, output, "MCP_ACTION_"+scenario.action)
		}
		runtimeTextModel(w, request, "CODEX_MCP_DONE_"+scenario.name, id+"-done")
		return
	}
	// 只能调用本次原生 CLI 真正声明的 namespace 和工具，不能硬发 Claude 平铺名。
	namespace, tool := "", ""
	for _, group := range parsed.Tools {
		if group.Type != "namespace" || group.Name != "mcp__"+scenario.server {
			continue
		}
		for _, candidate := range group.Tools {
			if candidate.Name == scenario.tool {
				namespace, tool = group.Name, candidate.Name
			}
		}
	}
	require.NotEmpty(t, namespace, "原生模型目录必须声明目标 MCP namespace")
	require.NotEmpty(t, tool)
	args := map[string]any{}
	if scenario.server == "manager" {
		args["value"] = "STDIO_MODEL_WRITE"
	}
	encoded, err := json.Marshal(args)
	require.NoError(t, err)
	w.Header().Set("Content-Type", "text/event-stream")
	event := func(kind string, value map[string]any) {
		value["type"] = kind
		data, err := json.Marshal(value)
		require.NoError(t, err)
		_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, data)
		require.NoError(t, err)
	}
	event("response.created", map[string]any{"response": map[string]any{"id": id}})
	event("response.output_item.done", map[string]any{"item": map[string]any{
		"type": "function_call", "namespace": namespace, "name": tool, "call_id": id, "arguments": string(encoded),
	}})
	event("response.completed", map[string]any{"response": map[string]any{"id": id,
		"usage": map[string]int{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15}}})
}

// 使用已有真实 MCP SDK HTTP 服务；回调真正写磁盘，结束时检查服务器收到的调用与错误。
func runtimeCodexMcpHTTP(t *testing.T, ctx context.Context, root, effect string) (string, func()) {
	t.Helper()
	adapter := filepath.Dir(filepath.Dir(os.Getenv("TYRS_HAND_TEST_CLAUDE_BIN")))
	report := filepath.Join(root, "mcp-http-report.json")
	code := fmt.Sprintf(`import {LocalMcp} from %q;
import {appendFile,writeFile} from 'node:fs/promises';
const fixture=new LocalMcp(()=>appendFile(%q,'HTTP_MODEL_WRITE\n'));
console.log(JSON.stringify({url:await fixture.start()}));
process.on('SIGTERM',async()=>{await fixture.close();await writeFile(%q,JSON.stringify({calls:fixture.calls,errors:fixture.errors}));process.exit(0)});`,
		filepath.Join(adapter, "dist", "test", "fixtures", "mcp-http.mjs"), effect, report)
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
		})
	}
	t.Cleanup(stop)
	var endpoint struct{ URL string }
	require.NoError(t, json.NewDecoder(stdout).Decode(&endpoint))
	check := func() {
		stop()
		data, err := os.ReadFile(report)
		require.NoError(t, err)
		var result struct {
			Calls  int
			Errors []string
		}
		require.NoError(t, json.Unmarshal(data, &result))
		require.Equal(t, 1, result.Calls)
		require.Empty(t, result.Errors)
	}
	return endpoint.URL, check
}

func verifyRuntimeCodexMcp(t *testing.T, ctx context.Context, registry *RuntimeRegistry, connection *ssh.Client, fixture *runtimeCodexMcpFixture, root string) {
	t.Helper()
	claudeGeneration := registry.entries[runtimeidentity.Claude].Runtime.Generation()
	project := filepath.Join(root, "project")
	node, err := exec.LookPath("node")
	require.NoError(t, err)
	adapter := filepath.Dir(filepath.Dir(os.Getenv("TYRS_HAND_TEST_CLAUDE_BIN")))
	stdio := filepath.Join(project, "mcp-stdio-effect.txt")
	httpEffect := filepath.Join(project, "mcp-http-effect.txt")
	httpURL, checkHTTP := runtimeCodexMcpHTTP(t, ctx, root, httpEffect)
	manager := runtimeCodexMcpManager(t, root, adapter)
	if t.Name() == "TestRuntimeCodexMcpPaginationRealSSH" {
		manager = runtimeCodexMcpPaginationRelay(t, root, adapter)
	}
	servers := map[string]any{
		"manager": map[string]any{"command": node, "args": []string{manager},
			"env": map[string]string{"FIXTURE_MARKER": "MCP_MARKER_BEFORE_RELOAD", "FIXTURE_EFFECT_PATH": stdio}},
		"httpfixture": map[string]any{"url": httpURL, "http_headers": map[string]string{"Authorization": "Bearer test-not-a-secret", "X-Runtime": "claude-fixture"}},
	}
	for _, mode := range []string{"form", "url"} {
		servers[mode+"fixture"] = map[string]any{"command": node, "args": []string{filepath.Join(adapter, "test", "fixtures", "mcp-interactive-server.mjs")},
			"env": map[string]string{"FIXTURE_ELICITATION_MODE": mode, "FIXTURE_EFFECT_PATH": filepath.Join(project, "mcp-"+mode+"-effect.txt")}}
	}
	client, _ := connectRuntimeSSHWithTrace(t, ctx, connection, runtimeidentity.Codex, codex.SocketClientOptions{})
	require.NoError(t, client.Call(ctx, "config/value/write", map[string]any{"keyPath": "mcp_servers", "value": servers, "mergeStrategy": "replace"}, nil))
	require.NoError(t, client.Call(ctx, "config/value/write", map[string]any{"keyPath": "mcp_oauth_credentials_store", "value": "file", "mergeStrategy": "replace"}, nil))
	startup := client.Subscribe(codex.ThreadFilter{})
	defer startup.Close()
	thread := readSessionThread(t, ctx, client, "thread/start", map[string]any{"cwd": project, "approvalPolicy": "never", "sandbox": "danger-full-access"})
	runtimeCodexMcpReady(t, ctx, startup, thread.ID, len(servers))
	if t.Name() == "TestRuntimeCodexMcpPaginationRealSSH" {
		runtimeCodexMcpPagination(t, ctx, client, thread.ID, root)
		require.Zero(t, fixture.calls.Load())
		return
	}
	runtimeCodexMcpManagement(t, ctx, client, thread.ID, stdio, servers)
	require.Zero(t, fixture.calls.Load(), "目录、资源、配置重载和工具 RPC 不得请求模型")
	for _, scenario := range runtimeCodexMcpScenarios {
		if !t.Run(scenario.name, func(t *testing.T) {
			runtimeCodexMcpTurn(t, ctx, connection, fixture, scenario)
		}) {
			return
		}
	}
	require.Equal(t, int64(16), fixture.calls.Load())
	runtimeCodexMcpFile(t, stdio, "RPC_BEFORE\nRPC_AFTER\nSTDIO_MODEL_WRITE\n")
	runtimeCodexMcpFile(t, httpEffect, "HTTP_MODEL_WRITE\n")
	runtimeCodexMcpFile(t, filepath.Join(project, "mcp-form-effect.txt"), "FORM_CONFIRMED\n")
	runtimeCodexMcpFile(t, filepath.Join(project, "mcp-url-effect.txt"), "URL_CONFIRMED\n")
	checkHTTP()
	require.Equal(t, claudeGeneration, registry.entries[runtimeidentity.Claude].Runtime.Generation(), "Codex MCP 配置重载不得重启 Claude")
}

func runtimeCodexMcpReady(t *testing.T, ctx context.Context, events *codex.EventSubscription, threadID string, count int) {
	t.Helper()
	ready := map[string]bool{}
	for len(ready) != count {
		select {
		case <-ctx.Done():
			t.Fatalf("真实 MCP 启动通知不完整：%d/%d", len(ready), count)
		case event, ok := <-events.Events():
			require.True(t, ok)
			if event.Method != "mcpServer/startupStatus/updated" {
				continue
			}
			var status struct {
				ThreadID, Name, Status string
				Error                  *string
			}
			require.NoError(t, json.Unmarshal(event.Params, &status))
			if status.ThreadID != threadID {
				continue
			}
			require.NotContains(t, []string{"failed", "cancelled"}, status.Status, "服务 %s 启动失败", status.Name)
			if status.Status == "ready" {
				require.Nil(t, status.Error)
				ready[status.Name] = true
			}
		}
	}
}

func runtimeCodexMcpFile(t *testing.T, path, expected string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, expected, string(data), "真实 MCP 文件副作用必须精确一致")
}

func runtimeCodexMcpManagement(t *testing.T, ctx context.Context, client *codex.SocketClient, threadID, effect string, servers map[string]any) {
	t.Helper()
	seen := map[string]bool{}
	cursor := ""
	for {
		params := map[string]any{"threadId": threadID, "limit": 1, "detail": "full"}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var page struct {
			Data []struct {
				Name              string
				ServerInfo        struct{ Name string }
				Tools             map[string]json.RawMessage
				Resources         []struct{ URI string }
				ResourceTemplates []struct{ URITemplate string }
			}
			NextCursor *string
		}
		require.NoError(t, client.Call(ctx, "mcpServerStatus/list", params, &page))
		require.Len(t, page.Data, 1)
		server := page.Data[0]
		require.Contains(t, servers, server.Name)
		require.False(t, seen[server.Name], "分页不能重复返回服务")
		seen[server.Name] = true
		if server.Name == "manager" {
			require.Equal(t, "MCP_MARKER_BEFORE_RELOAD", server.ServerInfo.Name)
			require.Contains(t, server.Tools, "append")
			require.Contains(t, server.Tools, "fail")
			require.Len(t, server.Resources, 2)
			require.Len(t, server.ResourceTemplates, 1)
			require.Equal(t, "fixture://{name}", server.ResourceTemplates[0].URITemplate)
		}
		if page.NextCursor == nil {
			break
		}
		require.NotEqual(t, cursor, *page.NextCursor)
		cursor = *page.NextCursor
	}
	require.Len(t, seen, len(servers))
	read := func(marker string) {
		var result struct {
			Contents []struct{ URI, MimeType, Text string }
		}
		require.NoError(t, client.Call(ctx, "mcpServer/resource/read", map[string]any{"threadId": threadID, "server": "manager", "uri": "fixture://first"}, &result))
		require.Len(t, result.Contents, 1)
		require.Equal(t, "fixture://first", result.Contents[0].URI)
		require.Equal(t, "text/plain", result.Contents[0].MimeType)
		require.Equal(t, marker, result.Contents[0].Text)
	}
	call := func(tool, value, marker, content string) {
		meta := map[string]any{"fixture": "codex-real-ssh"}
		var result struct {
			Content           []struct{ Type, Text string }
			StructuredContent struct{ Marker, Content string }
			IsError           bool
			Meta              map[string]any `json:"_meta"`
		}
		require.NoError(t, client.Call(ctx, "mcpServer/tool/call", map[string]any{"threadId": threadID, "server": "manager", "tool": tool,
			"arguments": map[string]string{"value": value}, "_meta": meta}, &result))
		require.Equal(t, tool == "fail", result.IsError)
		require.Len(t, result.Content, 1)
		require.Equal(t, marker, result.Content[0].Text)
		if tool != "fail" {
			require.Equal(t, meta["fixture"], result.Meta["fixture"])
			require.Equal(t, threadID, result.Meta["threadId"])
			require.Contains(t, result.Meta, "progressToken")
			require.Equal(t, marker, result.StructuredContent.Marker)
			require.Equal(t, content, result.StructuredContent.Content)
		}
		runtimeCodexMcpFile(t, effect, content)
	}
	read("MCP_MARKER_BEFORE_RELOAD")
	call("append", "RPC_BEFORE", "MCP_MARKER_BEFORE_RELOAD", "RPC_BEFORE\n")
	call("fail", "NO_WRITE", "FIXTURE_TOOL_ERROR", "RPC_BEFORE\n")
	servers["manager"].(map[string]any)["env"].(map[string]string)["FIXTURE_MARKER"] = "MCP_MARKER_AFTER_RELOAD"
	require.NoError(t, client.Call(ctx, "config/value/write", map[string]any{"keyPath": "mcp_servers", "value": servers, "mergeStrategy": "replace"}, nil))
	require.NoError(t, client.Call(ctx, "config/mcpServer/reload", nil, nil))
	// 同一既有会话必须重新连接真正修改过的服务，不能只核配置写入返回值。
	read("MCP_MARKER_AFTER_RELOAD")
	call("append", "RPC_AFTER", "MCP_MARKER_AFTER_RELOAD", "RPC_BEFORE\nRPC_AFTER\n")
}

func runtimeCodexMcpTurn(t *testing.T, ctx context.Context, connection *ssh.Client, fixture *runtimeCodexMcpFixture, scenario runtimeCodexMcpScenario) {
	t.Helper()
	effectKind := strings.SplitN(scenario.name, "-", 2)[0]
	effectPath := filepath.Join(fixture.root, "project", "mcp-"+effectKind+"-effect.txt")
	beforePermission, beforePermissionErr := os.ReadFile(effectPath)
	if beforePermissionErr != nil {
		require.True(t, os.IsNotExist(beforePermissionErr))
	}
	prompts := make(chan runtimeApprovalPrompt, 1)
	client := connectRuntimeSSHWithOptions(t, ctx, connection, runtimeidentity.Codex, codex.SocketClientOptions{
		ServerRequestHandler: func(callbackCtx context.Context, request codex.ServerRequest) (any, error) {
			t.Logf("真实 MCP 回调：%s", request.Method)
			prompt := runtimeApprovalPrompt{request: request, reply: make(chan any, 1), cancelled: make(chan struct{})}
			select {
			case prompts <- prompt:
			case <-callbackCtx.Done():
				return nil, callbackCtx.Err()
			}
			select {
			case reply := <-prompt.reply:
				return reply, nil
			case <-callbackCtx.Done():
				close(prompt.cancelled)
				return nil, callbackCtx.Err()
			}
		},
	})
	thread := readSessionThread(t, ctx, client, "thread/start", map[string]any{
		"cwd": filepath.Join(fixture.root, "project"), "approvalPolicy": "on-request", "sandbox": "danger-full-access",
	})
	events := client.Subscribe(codex.ThreadFilter{ThreadID: thread.ID})
	defer events.Close()
	var started struct{ Turn struct{ ID string } }
	require.NoError(t, client.Call(ctx, "turn/start", map[string]any{"threadId": thread.ID,
		"input": []map[string]string{{"type": "text", "text": "CODEX_MCP_" + scenario.name}}}, &started))
	// 原生 Codex 先用 elicitation 请求工具执行授权，然后才转发 MCP 服务自己的表单或 URL。
	var approval runtimeApprovalPrompt
	select {
	case approval = <-prompts:
	case <-ctx.Done():
		t.Fatal("真实 MCP 工具执行授权未到达 SSH 客户端")
	}
	require.Equal(t, "mcpServer/elicitation/request", approval.request.Method)
	var permission struct {
		ThreadID, TurnID, ServerName, Mode string
		Meta                               map[string]any `json:"_meta"`
	}
	require.NoError(t, json.Unmarshal(approval.request.Params, &permission))
	require.Equal(t, thread.ID, permission.ThreadID)
	require.Equal(t, started.Turn.ID, permission.TurnID)
	require.Equal(t, scenario.server, permission.ServerName)
	require.Equal(t, "form", permission.Mode)
	require.Equal(t, "mcp_tool_call", permission.Meta["codex_approval_kind"])
	beforeAnswer, beforeAnswerErr := os.ReadFile(effectPath)
	require.Equal(t, os.IsNotExist(beforePermissionErr), os.IsNotExist(beforeAnswerErr))
	require.Equal(t, beforePermission, beforeAnswer, "工具执行授权之前不得产生文件副作用")
	approval.reply <- map[string]any{"action": "accept", "content": map[string]any{}, "_meta": nil}
	if scenario.action != "" {
		var prompt runtimeApprovalPrompt
		select {
		case prompt = <-prompts:
		case <-ctx.Done():
			t.Fatal("真实 MCP elicitation 未到达 SSH 客户端")
		}
		require.Equal(t, "mcpServer/elicitation/request", prompt.request.Method)
		var request struct {
			ThreadID, TurnID, ServerName, Mode, URL, ElicitationID string
			RequestedSchema                                        struct {
				Required   []string
				Properties map[string]struct{ Type string }
			}
		}
		require.NoError(t, json.Unmarshal(prompt.request.Params, &request))
		require.Equal(t, thread.ID, request.ThreadID)
		require.Equal(t, started.Turn.ID, request.TurnID)
		require.Equal(t, scenario.server, request.ServerName)
		mode := strings.SplitN(scenario.name, "-", 2)[0]
		require.Equal(t, mode, request.Mode)
		path := filepath.Join(fixture.root, "project", "mcp-"+mode+"-effect.txt")
		before, err := os.ReadFile(path)
		if scenario.action == "accept" {
			require.True(t, os.IsNotExist(err), "回答前不得执行 MCP 工具")
		} else {
			require.NoError(t, err)
		}
		var content any
		if mode == "form" {
			require.Equal(t, []string{"value"}, request.RequestedSchema.Required)
			require.Equal(t, "string", request.RequestedSchema.Properties["value"].Type)
			if scenario.action == "accept" {
				content = map[string]string{"value": "FORM_CONFIRMED"}
			}
		} else {
			require.Equal(t, "http://127.0.0.1/fixture", request.URL)
			require.Equal(t, "fixture-browser-flow", request.ElicitationID)
		}
		prompt.reply <- map[string]any{"action": scenario.action, "content": content, "_meta": map[string]string{"source": "real-ssh"}}
		defer func() {
			if scenario.action == "accept" {
				runtimeCodexMcpFile(t, path, strings.ToUpper(mode)+"_CONFIRMED\n")
			} else {
				runtimeCodexMcpFile(t, path, string(before))
			}
		}()
	}
	var toolItem json.RawMessage
	finished := false
	for !finished {
		select {
		case <-ctx.Done():
			t.Fatal("真实 MCP 工具 Turn 未结束")
		case event, ok := <-events.Events():
			require.True(t, ok)
			var params struct {
				ThreadID, TurnID string
				Item             json.RawMessage
				Turn             struct {
					ID, Status string
					Error      any
				}
			}
			if event.Method != "item/completed" && event.Method != "turn/completed" {
				continue
			}
			require.NoError(t, json.Unmarshal(event.Params, &params))
			require.Equal(t, thread.ID, params.ThreadID)
			if event.Method == "turn/completed" {
				require.Equal(t, started.Turn.ID, params.Turn.ID)
				require.Equal(t, "completed", params.Turn.Status)
				require.Nil(t, params.Turn.Error)
				finished = true
				continue
			}
			var item struct {
				Type, Server, Tool, Status string
				Result, Error              json.RawMessage
			}
			require.NoError(t, json.Unmarshal(params.Item, &item))
			if item.Type != "mcpToolCall" {
				continue
			}
			require.Empty(t, toolItem, "每轮只能执行一次 MCP 工具")
			require.Equal(t, started.Turn.ID, params.TurnID)
			require.Equal(t, scenario.server, item.Server)
			require.Equal(t, scenario.tool, item.Tool)
			expectedStatus := "completed"
			if scenario.name == "stdio-error" {
				expectedStatus = "failed"
			}
			require.Equal(t, expectedStatus, item.Status)
			require.Equal(t, json.RawMessage("null"), item.Error)
			require.NotEqual(t, json.RawMessage("null"), item.Result)
			toolItem = params.Item
		}
	}
	require.NotEmpty(t, toolItem)
	var history struct {
		Thread struct {
			Turns []struct {
				ID    string
				Items []json.RawMessage
			}
		}
	}
	require.NoError(t, client.Call(ctx, "thread/read", map[string]any{"threadId": thread.ID, "includeTurns": true}, &history))
	matched, reply := false, false
	for _, turn := range history.Thread.Turns {
		if turn.ID != started.Turn.ID {
			continue
		}
		for _, item := range turn.Items {
			var value struct{ Type, Text string }
			require.NoError(t, json.Unmarshal(item, &value))
			if value.Type == "mcpToolCall" {
				require.JSONEq(t, string(toolItem), string(item))
				matched = true
			}
			if value.Type == "agentMessage" && value.Text == "CODEX_MCP_DONE_"+scenario.name {
				reply = true
			}
		}
	}
	require.True(t, matched, "原生历史必须保存真实 MCP 工具事件")
	require.True(t, reply, "原生历史必须保存工具回模后的回复")
	_, returned := fixture.outputs.Load(scenario.name)
	require.True(t, returned)
	select {
	case extra := <-prompts:
		t.Fatalf("意外重复询问：%s", extra.request.Method)
	default:
	}
}

// Codex 0.147 不遍历 MCP 服务内部的 tools/list nextCursor；该缺口已单独复现。
// 此服务把两种工具放在同一页，使工具副作用与 elicitation 专项不依赖该目录缺口。
// mcpServerStatus/list 的 app-server 分页仍在本专项以 limit=1 独立验证。
func runtimeCodexMcpManager(t *testing.T, root, adapter string) string {
	t.Helper()
	sdk := filepath.Join(adapter, "node_modules", "@modelcontextprotocol", "sdk", "dist", "esm")
	code := fmt.Sprintf(`import {appendFile,readFile} from 'node:fs/promises';
import {Server} from %q;
import {StdioServerTransport} from %q;
import {CallToolRequestSchema,ListToolsRequestSchema,ListResourcesRequestSchema,ListResourceTemplatesRequestSchema,ReadResourceRequestSchema} from %q;
const marker=process.env.FIXTURE_MARKER;
const server=new Server({name:marker,version:'1.0.0'},{capabilities:{tools:{},resources:{}}});
const tool=name=>({name,inputSchema:{type:'object',properties:{value:{type:'string'}},required:['value']}});
server.setRequestHandler(ListToolsRequestSchema,async()=>({tools:[tool('append'),tool('fail')]}));
server.setRequestHandler(ListResourcesRequestSchema,async()=>({resources:[{name:'first',uri:'fixture://first'},{name:'second',uri:'fixture://second'}]}));
server.setRequestHandler(ListResourceTemplatesRequestSchema,async()=>({resourceTemplates:[{name:'template',uriTemplate:'fixture://{name}'}]}));
server.setRequestHandler(ReadResourceRequestSchema,async r=>({contents:[{uri:r.params.uri,mimeType:'text/plain',text:marker}]}));
server.setRequestHandler(CallToolRequestSchema,async r=>{
 if(r.params.name==='fail')return {content:[{type:'text',text:'FIXTURE_TOOL_ERROR'}],isError:true};
 if(typeof r.params.arguments?.value!=='string')throw new Error('value 必须是字符串');
 await appendFile(process.env.FIXTURE_EFFECT_PATH,r.params.arguments.value+'\n');
 return {content:[{type:'text',text:marker}],structuredContent:{marker,content:await readFile(process.env.FIXTURE_EFFECT_PATH,'utf8')},isError:false,_meta:r.params._meta};
});
await server.connect(new StdioServerTransport());`, filepath.Join(sdk, "server", "index.js"), filepath.Join(sdk, "server", "stdio.js"), filepath.Join(sdk, "types.js"))
	path := filepath.Join(root, "codex-mcp-manager.mjs")
	require.NoError(t, os.WriteFile(path, []byte(code), 0o600))
	return path
}

// 仅中继真实 SDK 服务字节并记录目录协议摘要，不生产 MCP 答案。
func runtimeCodexMcpPaginationRelay(t *testing.T, root, adapter string) string {
	t.Helper()
	code := fmt.Sprintf(`import {spawn} from 'node:child_process';
import {appendFileSync} from 'node:fs';
import {createInterface} from 'node:readline';
const child=spawn(process.execPath,[%q],{stdio:['pipe','pipe','inherit'],env:process.env});
const pending=new Map();
const record=v=>appendFileSync(%q,JSON.stringify(v)+'\n');
createInterface({input:process.stdin}).on('line',line=>{
 const value=JSON.parse(line);
 if(value.method?.endsWith('/list')){pending.set(value.id,value.method);record({direction:'request',method:value.method,cursor:value.params?.cursor??null});}
 child.stdin.write(line+'\n');
});
createInterface({input:child.stdout}).on('line',line=>{
 const value=JSON.parse(line),method=pending.get(value.id);
 if(method&&value.result){const r=value.result;record({direction:'response',method,nextCursor:r.nextCursor??null,tools:r.tools?.map(t=>t.name),resources:r.resources?.map(r=>r.uri),resourceTemplates:r.resourceTemplates?.map(r=>r.uriTemplate)});pending.delete(value.id);}
 process.stdout.write(line+'\n');
});
process.stdin.on('end',()=>child.stdin.end());
process.on('SIGTERM',()=>child.kill('SIGTERM'));
child.on('exit',code=>process.exit(code??1));`, filepath.Join(adapter, "test", "fixtures", "mcp-management-server.mjs"), filepath.Join(root, "mcp-pagination-transcript.txt"))
	path := filepath.Join(root, "mcp-pagination-relay.mjs")
	require.NoError(t, os.WriteFile(path, []byte(code), 0o600))
	return path
}

func runtimeCodexMcpPagination(t *testing.T, ctx context.Context, client *codex.SocketClient, threadID, root string) {
	t.Helper()
	var page struct {
		Data []struct {
			Name              string
			Tools             map[string]json.RawMessage
			Resources         []struct{ URI string }
			ResourceTemplates []struct{ URITemplate string }
		}
	}
	require.NoError(t, client.Call(ctx, "mcpServerStatus/list", map[string]any{"threadId": threadID, "limit": 100, "detail": "full"}, &page))
	transcript, err := os.ReadFile(filepath.Join(root, "mcp-pagination-transcript.txt"))
	require.NoError(t, err)
	if directory := os.Getenv("PROTOCOL_ARTIFACT_DIR"); directory != "" {
		require.NoError(t, os.MkdirAll(directory, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(directory, "mcp-pagination-evidence.txt"), transcript, 0o600))
	}
	// 证明 SDK 服务确实收到首屏请求并返回非空 cursor，排除初始化失败或配置未加载。
	for _, expected := range []string{
		`"direction":"request","method":"tools/list","cursor":null`,
		`"direction":"response","method":"tools/list","nextCursor":"tools-second","tools":["append"]`,
		`"direction":"response","method":"resources/list","nextCursor":"resources-second","resources":["fixture://first"]`,
	} {
		require.Contains(t, string(transcript), expected)
	}
	found := false
	for _, server := range page.Data {
		if server.Name != "manager" {
			continue
		}
		found = true
		_, appendFound := server.Tools["append"]
		_, failFound := server.Tools["fail"]
		if !appendFound || !failFound {
			t.Error("原生 tools/list 分页遗漏：必须同时发现 append 和 fail")
		}
		if len(server.Resources) != 2 {
			t.Errorf("原生 resources/list 分页不完整：实际%d，期望2", len(server.Resources))
		}
		require.Len(t, server.ResourceTemplates, 1)
		require.Equal(t, "fixture://{name}", server.ResourceTemplates[0].URITemplate)
	}
	require.True(t, found)
}
