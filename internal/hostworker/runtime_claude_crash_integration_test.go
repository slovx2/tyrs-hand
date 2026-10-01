//go:build integration

package hostworker

import (
	"context"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

// 模型只脚本化文本；阻塞点用于在真实回合进行中杀死真实适配器或 CLI 进程。
type runtimeClaudeCrashFixture struct {
	mu      sync.Mutex
	calls   map[string]int
	blocked map[string]chan struct{}
	release map[string]chan struct{}
}

var runtimeClaudeCrashMarkers = []string{"CRASH_WARMUP", "CRASH_INFLIGHT", "CRASH_CODEX", "CRASH_AFTER", "CLI_CRASH", "CLI_AFTER"}

func newRuntimeClaudeCrashFixture() *runtimeClaudeCrashFixture {
	fixture := &runtimeClaudeCrashFixture{calls: map[string]int{},
		blocked: map[string]chan struct{}{}, release: map[string]chan struct{}{}}
	for _, marker := range []string{"CRASH_INFLIGHT", "CLI_CRASH"} {
		fixture.blocked[marker], fixture.release[marker] = make(chan struct{}), make(chan struct{})
	}
	return fixture
}

func (f *runtimeClaudeCrashFixture) model(t *testing.T, w http.ResponseWriter, request *http.Request, engine runtimeidentity.Engine, body []byte) {
	marker := ""
	for _, candidate := range runtimeClaudeCrashMarkers {
		if strings.Contains(string(body), candidate) {
			marker = candidate
		}
	}
	if marker == "" && engine == runtimeidentity.Codex {
		// Codex 原生后台标题请求不属于业务回合。
		runtimeTextModel(w, request, "CRASH_BACKGROUND", "crash-background")
		return
	}
	require.NotEmpty(t, marker, "只接受已脚本化的崩溃回合")
	require.Equal(t, marker == "CRASH_CODEX", engine == runtimeidentity.Codex, "回合只能请求所属引擎")
	f.mu.Lock()
	f.calls[marker]++
	count := f.calls[marker]
	blocked, release := f.blocked[marker], f.release[marker]
	f.mu.Unlock()
	if blocked != nil {
		require.Equal(t, 1, count, "崩溃后的恢复不能重放进行中的模型请求")
		close(blocked)
		select {
		case <-release:
		case <-request.Context().Done():
			return
		}
	}
	runtimeTextModel(w, request, marker+"_DONE", strings.ToLower(marker))
}

func (f *runtimeClaudeCrashFixture) waitBlocked(t *testing.T, ctx context.Context, marker string) {
	t.Helper()
	select {
	case <-f.blocked[marker]:
	case <-ctx.Done():
		t.Fatalf("%s 模型请求未到达", marker)
	}
}

// 仅检查适配器后代；宿主 npm 安装的固定 CLI 将原生程序命名为 claude.exe。
func runtimeClaudeCLIDescendants(t *testing.T, root int) []int {
	t.Helper()
	output, err := exec.Command("ps", "-A", "-o", "pid=,ppid=,args=").Output()
	require.NoError(t, err)
	children := map[int][]int{}
	args := map[int]string{}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		ppid, ppidErr := strconv.Atoi(fields[1])
		if pidErr != nil || ppidErr != nil {
			continue
		}
		children[ppid] = append(children[ppid], pid)
		args[pid] = strings.Join(fields[2:], " ")
	}
	var found []int
	queue := []int{root}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		for _, child := range children[pid] {
			queue = append(queue, child)
			executable := strings.Fields(args[child])[0]
			if strings.HasSuffix(executable, "/claude.exe") || strings.HasSuffix(executable, "/claude") || executable == "claude" {
				found = append(found, child)
			}
		}
	}
	return found
}

func runtimeClaudeTurnStatus(t *testing.T, ctx context.Context, client *codex.SocketClient, threadID, turnID string) string {
	t.Helper()
	for {
		thread := readSessionThread(t, ctx, client, "thread/read", map[string]any{"threadId": threadID, "includeTurns": true})
		for _, turn := range thread.Turns {
			if turn.ID == turnID && turn.Status != "inProgress" {
				return turn.Status
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("回合 %s 在崩溃后没有进入终态", turnID)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// FAILURE-002：真实适配器进程与原生 CLI 分别在回合进行中被 SIGKILL；Worker 自动恢复 Claude 运行代，
// 不影响 Codex，进行中回合进入终态且模型请求不重放，恢复后的会话可继续执行新回合。
func verifyRuntimeClaudeCrash(t *testing.T, ctx context.Context, registry *RuntimeRegistry, connections map[runtimeidentity.Engine]*ssh.Client, root string, fixture *runtimeClaudeCrashFixture) {
	t.Helper()
	claude, codexEntry := registry.entries[runtimeidentity.Claude], registry.entries[runtimeidentity.Codex]
	client := connectRuntimeSSH(t, ctx, connections[runtimeidentity.Claude], runtimeidentity.Claude)
	thread := readSessionThread(t, ctx, client, "thread/start", map[string]any{
		"cwd": root, "approvalPolicy": "never", "sandbox": "read-only"})
	start := func(client *codex.SocketClient, threadID, text string) string {
		var result struct{ Turn struct{ ID string } }
		require.NoError(t, client.Call(ctx, "turn/start", map[string]any{"threadId": threadID,
			"input": []map[string]string{{"type": "text", "text": text}}}, &result))
		return result.Turn.ID
	}
	warmup := start(client, thread.ID, "CRASH_WARMUP")
	require.Equal(t, "completed", runtimeClaudeTurnStatus(t, ctx, client, thread.ID, warmup))

	// 适配器进程崩溃：只杀主进程，模拟 OOM 等单进程终止，子进程由 Worker 的恢复清理。
	inflight := start(client, thread.ID, "CRASH_INFLIGHT")
	fixture.waitBlocked(t, ctx, "CRASH_INFLIGHT")
	generation, codexGeneration := claude.Runtime.Generation(), codexEntry.Runtime.Generation()
	claude.Runtime.mu.Lock()
	pid := claude.Runtime.current.command.Process.Pid
	claude.Runtime.mu.Unlock()
	require.NoError(t, syscall.Kill(pid, syscall.SIGKILL))
	require.Eventually(t, func() bool { return claude.Runtime.Generation() > generation }, 30*time.Second,
		50*time.Millisecond, "Worker 必须自动恢复崩溃的 Claude 运行代")
	close(fixture.release["CRASH_INFLIGHT"])
	require.Equal(t, codexGeneration, codexEntry.Runtime.Generation(), "Claude 崩溃不能重启 Codex")
	codexClient := connectRuntimeSSH(t, ctx, connections[runtimeidentity.Codex], runtimeidentity.Codex)
	codexThread := readSessionThread(t, ctx, codexClient, "thread/start", map[string]any{
		"cwd": root, "approvalPolicy": "never", "sandbox": "read-only"})
	events := codexClient.Subscribe(codex.ThreadFilter{ThreadID: codexThread.ID})
	codexTurn := start(codexClient, codexThread.ID, "CRASH_CODEX")
	waitIsolationTurnCompleted(t, ctx, events, codexThread.ID, codexTurn)
	events.Close()

	client = connectRuntimeSSH(t, ctx, connections[runtimeidentity.Claude], runtimeidentity.Claude)
	resumed := readSessionThread(t, ctx, client, "thread/resume", map[string]any{"threadId": thread.ID})
	require.Equal(t, thread.ID, resumed.ID)
	status := runtimeClaudeTurnStatus(t, ctx, client, thread.ID, inflight)
	require.NotEqual(t, "completed", status, "被崩溃中断的回合不能伪装成功")
	t.Logf("适配器 PID %d 被 SIGKILL，运行代 %d→%d，中断回合终态 %s", pid, generation, claude.Runtime.Generation(), status)
	after := start(client, thread.ID, "CRASH_AFTER")
	require.Equal(t, "completed", runtimeClaudeTurnStatus(t, ctx, client, thread.ID, after))

	// 原生 CLI 崩溃：适配器存活，回合必须以失败终结而不是悬挂，同会话可继续。
	generation = claude.Runtime.Generation()
	claude.Runtime.mu.Lock()
	adapter := claude.Runtime.current.command.Process.Pid
	claude.Runtime.mu.Unlock()
	crashed := start(client, thread.ID, "CLI_CRASH")
	fixture.waitBlocked(t, ctx, "CLI_CRASH")
	cli := runtimeClaudeCLIDescendants(t, adapter)
	require.NotEmpty(t, cli, "必须定位真实原生 Claude CLI 进程")
	t.Logf("SIGKILL 原生 Claude CLI 进程 %v", cli)
	for _, process := range cli {
		require.NoError(t, syscall.Kill(process, syscall.SIGKILL))
	}
	status = runtimeClaudeTurnStatus(t, ctx, client, thread.ID, crashed)
	close(fixture.release["CLI_CRASH"])
	require.Equal(t, "failed", status, "原生 CLI 崩溃的回合必须明确失败")
	require.Equal(t, generation, claude.Runtime.Generation(), "CLI 崩溃不能重启整个适配器")
	cliAfter := start(client, thread.ID, "CLI_AFTER")
	require.Equal(t, "completed", runtimeClaudeTurnStatus(t, ctx, client, thread.ID, cliAfter))

	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	for _, marker := range runtimeClaudeCrashMarkers {
		require.Equal(t, 1, fixture.calls[marker], "%s 只能请求一次模型", marker)
	}
}
