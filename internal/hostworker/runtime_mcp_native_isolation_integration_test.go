//go:build integration

package hostworker

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

// 最小 stdio MCP 服务：真实经 Claude CLI 启动，工具调用写入标识文件，不依赖测试进程内的状态。
const runtimeMcpNativeServer = `import { writeFileSync } from 'node:fs'
import { createInterface } from 'node:readline'
const [identity, effect] = process.argv.slice(2)
const send = message => process.stdout.write(JSON.stringify({ jsonrpc: '2.0', ...message }) + '\n')
createInterface({ input: process.stdin }).on('line', line => {
  const request = JSON.parse(line)
  if (request.id === undefined) return
  if (request.method === 'initialize') return send({ id: request.id, result: {
    protocolVersion: request.params.protocolVersion, capabilities: { tools: {} },
    serverInfo: { name: identity, version: '1.0.0' } } })
  if (request.method === 'tools/list') return send({ id: request.id, result: { tools: [{ name: 'effect',
    description: 'Write the MCP-003 effect file', inputSchema: { type: 'object', properties: {} } }] } })
  if (request.method === 'tools/call') {
    writeFileSync(effect, identity, { flag: 'a' })
    return send({ id: request.id, result: { content: [{ type: 'text', text: 'MCP003_EFFECT_OK' }] } })
  }
  if (request.method === 'ping') return send({ id: request.id, result: {} })
  send({ id: request.id, error: { code: -32601, message: 'method not found' } })
})
`

type runtimeMcpNativeFixture struct {
	root, projectA, projectB, effect string
	mu                               sync.Mutex
	steps                            map[string]int
	calls                            int
}

func newRuntimeMcpNativeFixture(root string) *runtimeMcpNativeFixture {
	return &runtimeMcpNativeFixture{root: root, projectA: filepath.Join(root, "mcp003-project-a"),
		projectB: filepath.Join(root, "mcp003-project-b"), effect: filepath.Join(root, "mcp003-effect.txt"),
		steps: map[string]int{}}
}

func (f *runtimeMcpNativeFixture) model(t *testing.T, w http.ResponseWriter, request *http.Request, engine runtimeidentity.Engine, body []byte) {
	marker := ""
	for _, candidate := range []string{"MCP003_CLAUDE_A", "MCP003_CLAUDE_B", "MCP003_CODEX"} {
		if strings.Contains(string(body), candidate) {
			marker = candidate
		}
	}
	if marker == "" {
		// Codex 原生后台标题请求不属于业务回合，但同样不能看到 Claude 项目的 MCP 工具。
		require.Equal(t, runtimeidentity.Codex, engine, "Claude 不能出现未脚本化的模型请求")
		require.NotContains(t, string(body), "mcp003", "Codex 不能读取 Claude 项目原生 MCP 配置")
		runtimeTextModel(w, request, "MCP003_BACKGROUND", "mcp003-background")
		return
	}
	f.mu.Lock()
	step := f.steps[marker]
	f.steps[marker] = step + 1
	f.calls++
	f.mu.Unlock()
	var tools struct{ Tools []struct{ Name string } }
	require.NoError(t, json.Unmarshal(body, &tools))
	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	const nativeTool = "mcp__mcp003_native__effect"
	switch {
	case marker == "MCP003_CLAUDE_A" && step == 0:
		require.Equal(t, runtimeidentity.Claude, engine)
		require.True(t, names[nativeTool], "项目原生 stdio MCP 工具必须进入 Claude 模型请求")
		runtimeClaudeToolModel(t, w, "mcp003-a", nativeTool, map[string]any{})
	case marker == "MCP003_CLAUDE_A" && step == 1:
		result := lastPermissionToolResult(t, body)
		require.True(t, result.seen)
		require.False(t, result.isError, "stdio MCP 工具必须真实执行成功: %s", result.text)
		require.Contains(t, result.text, "MCP003_EFFECT_OK", "真实 MCP 结果必须回到模型")
		runtimeTextModel(w, request, "MCP003_DONE", "mcp003-a-done")
	case marker == "MCP003_CLAUDE_B" && step == 0:
		require.Equal(t, runtimeidentity.Claude, engine)
		require.False(t, names[nativeTool], "另一项目不能继承项目 A 的原生 MCP 配置")
		runtimeTextModel(w, request, "MCP003_B_DONE", "mcp003-b")
	case marker == "MCP003_CODEX" && step == 0:
		require.Equal(t, runtimeidentity.Codex, engine)
		require.NotContains(t, string(body), "mcp003_native", "Codex 不能读取 Claude 项目原生 MCP 配置")
		runtimeTextModel(w, request, "MCP003_CODEX_DONE", "mcp003-codex")
	default:
		t.Errorf("未脚本化的 MCP-003 模型请求 %s#%d", marker, step)
		http.Error(w, "unexpected model request", http.StatusBadRequest)
	}
}

// MCP-003：项目原生 .mcp.json 的 stdio 服务器经真实 SSH 与 Claude CLI 启动并产生实际副作用，
// 配置不外溢到同 Worker 的其他项目或 Codex 引擎。
func verifyRuntimeMcpNativeIsolation(t *testing.T, ctx context.Context, connections map[runtimeidentity.Engine]*ssh.Client, fixture *runtimeMcpNativeFixture) {
	t.Helper()
	server := filepath.Join(fixture.root, "mcp003-server.mjs")
	require.NoError(t, os.WriteFile(server, []byte(runtimeMcpNativeServer), 0o644))
	for _, project := range []string{fixture.projectA, fixture.projectB} {
		require.NoError(t, os.MkdirAll(filepath.Join(project, ".claude"), 0o755))
	}
	config, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"mcp003_native": map[string]any{
		"type": "stdio", "command": "node", "args": []string{server, "claude-native", fixture.effect}}}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(fixture.projectA, ".mcp.json"), config, 0o644))
	// 项目原生 MCP 需由项目设置显式启用，这是 Claude 原生的信任边界。
	require.NoError(t, os.WriteFile(filepath.Join(fixture.projectA, ".claude", "settings.local.json"),
		[]byte(`{"enableAllProjectMcpServers": true}`), 0o644))
	run := func(engine runtimeidentity.Engine, cwd, text string) {
		client := connectRuntimeSSH(t, ctx, connections[engine], engine)
		thread := readSessionThread(t, ctx, client, "thread/start", map[string]any{
			"cwd": cwd, "approvalPolicy": "never", "sandbox": "danger-full-access"})
		// 以真实终态事件判断完成；固定 Codex 的 thread/read 不支持为此类会话列出回合。
		events := client.Subscribe(codex.ThreadFilter{ThreadID: thread.ID})
		defer events.Close()
		var started struct{ Turn struct{ ID string } }
		require.NoError(t, client.Call(ctx, "turn/start", map[string]any{"threadId": thread.ID,
			"input": []map[string]string{{"type": "text", "text": text}}}, &started))
		waitIsolationTurnCompleted(t, ctx, events, thread.ID, started.Turn.ID)
	}
	run(runtimeidentity.Claude, fixture.projectA, "MCP003_CLAUDE_A")
	data, err := os.ReadFile(fixture.effect)
	require.NoError(t, err)
	require.Equal(t, "claude-native", string(data), "stdio MCP 工具只能真实执行一次")
	run(runtimeidentity.Claude, fixture.projectB, "MCP003_CLAUDE_B")
	run(runtimeidentity.Codex, fixture.projectA, "MCP003_CODEX")
	data, err = os.ReadFile(fixture.effect)
	require.NoError(t, err)
	require.Equal(t, "claude-native", string(data), "其他项目与引擎不能再次执行该 MCP 工具")
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	require.Equal(t, 4, fixture.calls, "只执行已脚本化的业务模型请求")
}
