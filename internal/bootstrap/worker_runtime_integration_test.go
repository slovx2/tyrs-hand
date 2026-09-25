//go:build integration

package bootstrap

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

// 使用生产 InitializeWorker 和真实 Controller；模型只能访问隔离 Mock 服务。
func TestWorkerBootstrapRealSSHSharedBudgetAndGitTool(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 75*time.Second)
	defer cancel()
	bin, adapter := os.Getenv("TYRS_HAND_TEST_CODEX_BIN"), os.Getenv("TYRS_HAND_TEST_CLAUDE_BIN")
	require.NotEmpty(t, bin, "缺少固定 Codex CLI，不能 skip")
	require.NotEmpty(t, adapter, "缺少固定 Claude 适配器，不能 skip")
	root, err := os.MkdirTemp("/tmp", "worker-boot-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	t.Setenv("HOME", root)
	entered, release := make(chan struct{}), make(chan struct{})
	var claudeCalls, codexCalls atomic.Int64
	var toolResultSeen atomic.Bool
	var requestsMu sync.Mutex
	requests := map[runtimeidentity.Engine][]json.RawMessage{}
	t.Cleanup(func() {
		requestsMu.Lock()
		defer requestsMu.Unlock()
		for engine, payloads := range requests {
			saveBootstrapArtifact(t, "models", engine, map[string]any{"requests": payloads})
		}
	})
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		if r.URL.Path == "/v1/messages" || r.URL.Path == "/v1/responses" {
			body, _ = io.ReadAll(io.LimitReader(r.Body, 4<<20))
			engine := runtimeidentity.Codex
			if r.URL.Path == "/v1/messages" {
				engine = runtimeidentity.Claude
			}
			requestsMu.Lock()
			requests[engine] = append(requests[engine], json.RawMessage(body))
			requestsMu.Unlock()
		}
		if strings.Contains(r.URL.Path, "count_tokens") {
			_, _ = fmt.Fprint(w, `{"input_tokens":10}`)
			return
		}
		if r.URL.Path == "/api/hello" {
			_, _ = fmt.Fprint(w, `{}`)
			return
		}
		if r.URL.Path == "/v1/responses" {
			codexCalls.Add(1)
			bootstrapModelText(w, false)
			return
		}
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		var payload struct {
			Tools    []struct{ Name, Description string } `json:"tools"`
			Messages json.RawMessage                      `json:"messages"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Error(err)
			http.Error(w, "invalid model input", 400)
			return
		}
		if claudeCalls.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return
			}
			for _, tool := range payload.Tools {
				if strings.Contains(tool.Description, "[git.commit]") {
					bootstrapModelGitCommit(w, tool.Name)
					return
				}
			}
			t.Error("真实 Worker 未将 git.commit 注入 Claude 模型请求")
		} else {
			toolResultSeen.Store(strings.Contains(string(payload.Messages), "tool_result") &&
				strings.Contains(string(payload.Messages), "bootstrap protocol commit"))
		}
		bootstrapModelText(w, true)
	}))
	t.Cleanup(model.Close)
	cfg := config.Config{WorkerDisableControlSync: true, WorkerMaxConcurrentJobs: 1,
		WorkerHome: root, WorkerCodexHome: filepath.Join(root, "codex"),
		WorkerDataRoot: filepath.Join(root, "state"), WorkerWorkspaceRoot: filepath.Join(root, "project"),
		WorkerCredentialFile: filepath.Join(root, "credential"), WorkerAuthorizedKeysFile: filepath.Join(root, "authorized_keys"),
		WorkerSSHHostKeyFile: filepath.Join(root, "host_key"), WorkerSSHListenAddr: "127.0.0.1:0",
		WorkerClaudeSSHListenAddr: "127.0.0.1:0", WorkerClaudeEnabled: true, WorkerClaudeBin: adapter,
		CodexBin: bin, WorkerShell: "/bin/sh", ControlTimeout: 3 * time.Second,
		TurnIdleTimeout: time.Minute, TurnMaxDuration: time.Minute, HeartbeatInterval: time.Hour,
		SSHAgentDir: filepath.Join(root, "agent"), WorkerGlobalEnvFile: filepath.Join(root, "codex.env")}
	for _, path := range []string{cfg.WorkerCodexHome, cfg.WorkerWorkspaceRoot, cfg.ClaudeConfigDir()} {
		require.NoError(t, os.MkdirAll(path, 0o700))
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromKey(key)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cfg.WorkerAuthorizedKeysFile, ssh.MarshalAuthorizedKey(signer.PublicKey()), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(cfg.WorkerCodexHome, "config.toml"), []byte(fmt.Sprintf(`model="mock-model"
model_provider="mock"
approval_policy="never"
[model_providers.mock]
name="Mock"
base_url=%q
wire_api="responses"
supports_websockets=false
request_max_retries=0
stream_max_retries=0
`, model.URL+"/v1")), 0o600))
	settings, err := json.Marshal(map[string]any{"model": "mock-claude", "env": map[string]string{
		"ANTHROPIC_API_KEY": "mock-only", "ANTHROPIC_BASE_URL": model.URL,
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1"}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(cfg.ClaudeConfigDir(), "settings.json"), settings, 0o600))
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", cfg.WorkerWorkspaceRoot}, args...)...).CombinedOutput()
		require.NoError(t, err, "%s", out)
		return string(out)
	}
	git("init")
	git("config", "user.name", "Protocol Test")
	git("config", "user.email", "test@example.invalid")
	require.NoError(t, os.WriteFile(filepath.Join(cfg.WorkerWorkspaceRoot, "effect.txt"), []byte("verified tool effect"), 0o600))
	app, cleanup, err := InitializeWorker(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(cleanup)
	clients := map[runtimeidentity.Engine]*codex.SocketClient{}
	connectionsClosed := map[runtimeidentity.Engine]<-chan struct{}{}
	threads := map[runtimeidentity.Engine]string{}
	for _, engine := range []runtimeidentity.Engine{runtimeidentity.Codex, runtimeidentity.Claude} {
		entry, err := app.Runtimes.Entry(engine)
		require.NoError(t, err)
		require.Equal(t, "running", entry.Runtime.Info().Status)
		client, closed := connectBootstrapSSH(t, ctx, entry, signer)
		clients[engine] = client
		connectionsClosed[engine] = closed
		var result struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
		}
		require.NoError(t, client.Call(ctx, "thread/start", map[string]any{
			"cwd": cfg.WorkerWorkspaceRoot, "approvalPolicy": "never", "sandbox": "danger-full-access"}, &result))
		threads[engine] = result.Thread.ID
	}
	start := func(engine runtimeidentity.Engine) error {
		var result any
		return clients[engine].Call(ctx, "turn/start", map[string]any{"threadId": threads[engine],
			"input": []map[string]any{{"type": "text", "text": "run protocol tool", "text_elements": []any{}}}}, &result)
	}
	claudeEvents := clients[runtimeidentity.Claude].Subscribe(codex.ThreadFilter{ThreadID: threads[runtimeidentity.Claude]})
	t.Cleanup(claudeEvents.Close)
	require.NoError(t, start(runtimeidentity.Claude))
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("Claude 没有到达 Mock LLM")
	}
	err = start(runtimeidentity.Codex)
	require.ErrorContains(t, err, "并发上限")
	require.Zero(t, codexCalls.Load(), "被拒绝任务不能到达模型")
	close(release)
	awaitBootstrapTurn(t, ctx, claudeEvents)
	require.True(t, toolResultSeen.Load(), "工具结果必须回到真实 SDK 的下一轮模型请求")
	require.Equal(t, "bootstrap protocol commit\n", git("log", "-1", "--format=%s"))
	require.Equal(t, "verified tool effect", git("show", "HEAD:effect.txt"))
	saveBootstrapArtifact(t, "effects", runtimeidentity.Claude, map[string]any{
		"gitCommit":            strings.TrimSpace(git("rev-parse", "HEAD")),
		"committedFileContent": git("show", "HEAD:effect.txt"), "toolResultSeen": toolResultSeen.Load(),
	})
	codexEvents := clients[runtimeidentity.Codex].Subscribe(codex.ThreadFilter{ThreadID: threads[runtimeidentity.Codex]})
	t.Cleanup(codexEvents.Close)
	require.Eventually(t, func() bool { return start(runtimeidentity.Codex) == nil }, 3*time.Second, 20*time.Millisecond)
	awaitBootstrapTurn(t, ctx, codexEvents)
	require.EqualValues(t, 1, codexCalls.Load())

	// 共用授权文件更新后，两端旧连接都关闭，新密钥仍能读取原会话。
	_, replacement, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	nextSigner, err := ssh.NewSignerFromKey(replacement)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cfg.WorkerAuthorizedKeysFile, ssh.MarshalAuthorizedKey(nextSigner.PublicKey()), 0o600))
	for _, engine := range []runtimeidentity.Engine{runtimeidentity.Codex, runtimeidentity.Claude} {
		select {
		case <-connectionsClosed[engine]:
		case <-time.After(3 * time.Second):
			t.Fatal("共用授权变更没有关闭旧 SSH 连接")
		}
		entry, err := app.Runtimes.Entry(engine)
		require.NoError(t, err)
		fresh, _ := connectBootstrapSSH(t, ctx, entry, nextSigner)
		var result any
		require.NoError(t, fresh.Call(ctx, "thread/read", map[string]any{"threadId": threads[engine]}, &result))
		require.Equal(t, "running", entry.Runtime.Info().Status, "授权撤销不能重启引擎或删除会话")
	}
}
