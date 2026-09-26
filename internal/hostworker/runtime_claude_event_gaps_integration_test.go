//go:build integration

package hostworker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/stretchr/testify/require"
)

func TestRuntimeClaudeEventGapsRealSSH(t *testing.T) {
	testRuntimeRegistryRealSSH(t, "claude-event-gaps")
}

type runtimeClaudeEventGapsFixture struct {
	root  string
	calls atomic.Int64
	reset atomic.Int64
}

const claudeUsageWarning = "Claude usage is nearing the five hour limit (95% used)."

func (f *runtimeClaudeEventGapsFixture) model(t *testing.T, w http.ResponseWriter, request *http.Request, body []byte) {
	t.Helper()
	step := f.calls.Add(1)
	require.LessOrEqual(t, step, int64(7), "真实工具以外不得增加模型续写")
	var modelRequest struct {
		Tools    []struct{ Name string }
		Messages []struct {
			Role    string
			Content json.RawMessage
		}
	}
	require.NoError(t, json.Unmarshal(body, &modelRequest))
	scenario := "mutate"
	if step == 5 {
		scenario = "clean"
	} else if step >= 6 {
		scenario = "readonly"
	}
	markerSeen := false
	for _, message := range modelRequest.Messages {
		markerSeen = markerSeen || message.Role == "user" && strings.Contains(string(message.Content), "EVENTS_010_"+scenario)
	}
	require.True(t, markerSeen, "模型请求必须来自指定场景的真实 Turn")
	if step == 2 || step == 3 || step == 4 || step == 7 {
		f.verifyToolResult(t, body, fmt.Sprintf("toolu_events_010_%d", step-1), step == 7)
	}
	// 由固定 CLI 从真实 HTTP 响应头产生 SDK rate_limit_event，禁止直接注入 warning。
	f.reset.CompareAndSwap(0, time.Now().Unix()+3600)
	w.Header().Set("anthropic-ratelimit-unified-status", "allowed_warning")
	w.Header().Set("anthropic-ratelimit-unified-reset", strconv.FormatInt(f.reset.Load(), 10))
	w.Header().Set("anthropic-ratelimit-unified-5h-utilization", "0.95")
	w.Header().Set("anthropic-ratelimit-unified-5h-surpassed-threshold", "0.9")
	w.Header().Set("anthropic-ratelimit-unified-representative-claim", "five_hour")
	if step == 4 || step == 5 || step == 7 {
		runtimeTextModel(w, request, "EVENTS_010_"+scenario+"_DONE", fmt.Sprintf("events-010-%d", step))
		return
	}
	cwd := filepath.Join(f.root, "project", "events010-"+scenario)
	tool := "Read"
	input := map[string]any{"file_path": filepath.Join(cwd, "tracked.txt")}
	switch step {
	case 2:
		tool = "Edit"
		input["old_string"], input["new_string"] = "BEFORE", "ACTUAL_AFTER"
	case 3:
		tool, input = "Write", map[string]any{"file_path": filepath.Join(cwd, "created.txt"), "content": "ACTUAL_CREATED\n"}
	case 6:
		tool, input = "Write", map[string]any{"file_path": filepath.Join(cwd, "forbidden.txt"), "content": "FORBIDDEN\n"}
	}
	declared := false
	for _, candidate := range modelRequest.Tools {
		declared = declared || candidate.Name == tool
	}
	require.True(t, declared, "必须使用真实 CLI 声明的业务工具")
	w.Header().Set("Content-Type", "text/event-stream")
	event := func(kind string, value map[string]any) {
		value["type"] = kind
		encoded, err := json.Marshal(value)
		require.NoError(t, err)
		_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, encoded)
		require.NoError(t, err)
		w.(http.Flusher).Flush()
	}
	event("message_start", map[string]any{"message": map[string]any{
		"id": fmt.Sprintf("msg_events_010_%d", step), "type": "message", "role": "assistant", "model": "claude-sonnet-4-6",
		"content": []any{}, "stop_reason": nil, "usage": map[string]int{"input_tokens": 10, "output_tokens": 1},
	}})
	event("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{
		"type": "tool_use", "id": fmt.Sprintf("toolu_events_010_%d", step), "name": tool, "input": map[string]any{},
	}})
	encoded, err := json.Marshal(input)
	require.NoError(t, err)
	event("content_block_delta", map[string]any{"index": 0, "delta": map[string]string{"type": "input_json_delta", "partial_json": string(encoded)}})
	event("content_block_stop", map[string]any{"index": 0})
	event("message_delta", map[string]any{"delta": map[string]any{"stop_reason": "tool_use"}, "usage": map[string]int{"output_tokens": 10}})
	event("message_stop", map[string]any{})
}

func (f *runtimeClaudeEventGapsFixture) verifyToolResult(t *testing.T, body []byte, id string, failed bool) {
	t.Helper()
	var request struct {
		Messages []struct{ Content json.RawMessage }
	}
	require.NoError(t, json.Unmarshal(body, &request))
	matches := 0
	for _, message := range request.Messages {
		var blocks []struct {
			Type      string
			ToolUseID string `json:"tool_use_id"`
			IsError   bool   `json:"is_error"`
			Content   json.RawMessage
		}
		if json.Unmarshal(message.Content, &blocks) != nil {
			continue
		}
		for _, block := range blocks {
			if block.Type == "tool_result" && block.ToolUseID == id {
				matches++
				require.Equal(t, failed, block.IsError, "真实工具成功或拒绝必须如实回到模型")
				require.NotEmpty(t, block.Content)
			}
		}
	}
	require.Equal(t, 1, matches, "工具结果只能入模一次")
}

func prepareClaudeEventGit(t *testing.T, ctx context.Context, root, scenario string) string {
	t.Helper()
	cwd := filepath.Join(root, "project", "events010-"+scenario)
	require.NoError(t, os.MkdirAll(cwd, 0o700))
	git := func(args ...string) {
		claudeEventGitOutput(t, ctx, root, cwd, args...)
	}
	git("init", "--initial-branch=main")
	require.NoError(t, os.WriteFile(filepath.Join(cwd, "tracked.txt"), []byte("KEEP\nBEFORE\nTAIL\n"), 0o600))
	git("add", "tracked.txt")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "fixture")
	return cwd
}

func claudeEventGitOutput(t *testing.T, ctx context.Context, root, cwd string, args ...string) string {
	t.Helper()
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = cwd
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	output, err := command.CombinedOutput()
	require.NoError(t, err, "临时 Git fixture 失败: %s", output)
	return string(output)
}

// EVENTS-010：工作区 diff 与原生限额 warning 的窄验收，不代表 EVENTS-001 整体完成。
func verifyRuntimeClaudeEventGaps(t *testing.T, ctx context.Context, client *codex.SocketClient, fixture *runtimeClaudeEventGapsFixture) {
	t.Helper()
	for _, scenario := range []string{"mutate", "clean", "readonly"} {
		if !t.Run(scenario, func(t *testing.T) {
			// 仅在隔离的项目子目录初始化 Git，runtime HOME、凭据和 SSH 密钥均在仓库之外。
			cwd := prepareClaudeEventGit(t, ctx, fixture.root, scenario)
			sandbox := "danger-full-access"
			if scenario == "readonly" {
				sandbox = "read-only"
			}
			thread := readSessionThread(t, ctx, client, "thread/start", map[string]any{
				"cwd": cwd, "approvalPolicy": "never", "sandbox": sandbox,
			})
			events := client.Subscribe(codex.ThreadFilter{ThreadID: thread.ID})
			defer events.Close()
			var started struct{ Turn struct{ ID string } }
			require.NoError(t, client.Call(ctx, "turn/start", map[string]any{"threadId": thread.ID,
				"input": []map[string]string{{"type": "text", "text": "EVENTS_010_" + scenario}}}, &started))
			diffs, completed := collectClaudeEventGaps(t, ctx, events, thread.ID, started.Turn.ID, scenario)
			history := readClaudeEventTurn(t, ctx, client, thread.ID)
			require.Equal(t, started.Turn.ID, history.ID)
			require.Equal(t, "completed", history.Status)
			verifyClaudeEventGapHistory(t, history, completed, scenario, cwd)
			tracked, err := os.ReadFile(filepath.Join(cwd, "tracked.txt"))
			require.NoError(t, err)
			gitStatus := claudeEventGitOutput(t, ctx, fixture.root, cwd, "status", "--porcelain=v1", "--untracked-files=all")
			if scenario == "mutate" {
				require.Equal(t, " M tracked.txt\n?? created.txt\n", gitStatus, "真实 Git 必须分别识别已跟踪修改和未跟踪新文件")
				require.Equal(t, "KEEP\nACTUAL_AFTER\nTAIL\n", string(tracked))
				created, err := os.ReadFile(filepath.Join(cwd, "created.txt"))
				require.NoError(t, err)
				require.Equal(t, "ACTUAL_CREATED\n", string(created))
				require.NotEmpty(t, diffs, "真实 Git 变更必须发出 diff")
				verifyClaudeActualDiff(t, diffs[len(diffs)-1])
			} else {
				require.Empty(t, gitStatus, "无变更与只读拒绝场景的真实 Git 工作区必须保持干净")
				require.Equal(t, "KEEP\nBEFORE\nTAIL\n", string(tracked))
				require.Empty(t, diffs, "无变更或只读拒绝不得伪造工作区 diff")
				for _, name := range []string{"created.txt", "forbidden.txt"} {
					_, err := os.Stat(filepath.Join(cwd, name))
					require.True(t, os.IsNotExist(err), "无授权副作用不得落盘")
				}
			}
		}) {
			return
		}
	}
	require.Equal(t, int64(7), fixture.calls.Load())
}

func collectClaudeEventGaps(t *testing.T, ctx context.Context, events *codex.EventSubscription, threadID, turnID, scenario string) ([]string, map[string]json.RawMessage) {
	t.Helper()
	diffs, warnings := []string{}, []string{}
	completed := map[string]json.RawMessage{}
	for {
		select {
		case <-ctx.Done():
			t.Fatal("真实 Claude diff/warning 验收未收到 Turn 终态")
		case event, ok := <-events.Events():
			require.True(t, ok)
			var params struct {
				ThreadID, TurnID, Diff, Message, Delta string
				Item                                   json.RawMessage
				Turn                                   struct {
					ID, Status string
					Error      any
				}
			}
			require.NoError(t, json.Unmarshal(event.Params, &params))
			require.Equal(t, threadID, params.ThreadID)
			if params.TurnID != "" {
				require.Equal(t, turnID, params.TurnID)
			}
			switch event.Method {
			case "warning":
				warnings = append(warnings, params.Message)
			case "turn/diff/updated":
				require.Equal(t, turnID, params.TurnID)
				require.Equal(t, "mutate", scenario)
				require.NotEmpty(t, params.Diff)
				require.Contains(t, params.Diff, "+ACTUAL_AFTER")
				diffs = append(diffs, params.Diff)
			case "item/agentMessage/delta", "item/reasoning/textDelta", "item/reasoning/summaryTextDelta":
				require.NotContains(t, params.Delta, "Claude usage", "原生警告不得污染助手或推理流")
			case "item/completed":
				var item struct{ ID string }
				require.NoError(t, json.Unmarshal(params.Item, &item))
				require.NotContains(t, completed, item.ID, "Item 终态不能重复")
				completed[item.ID] = params.Item
			case "turn/completed":
				require.Equal(t, turnID, params.Turn.ID)
				require.Equal(t, "completed", params.Turn.Status)
				require.Nil(t, params.Turn.Error)
				// mutate 四次、readonly 两次真实 HTTP 相同限额头，在同一 Turn 只能产生一次警告。
				require.Equal(t, []string{claudeUsageWarning}, warnings)
				return diffs, completed
			}
		}
	}
}

func verifyClaudeActualDiff(t *testing.T, diff string) {
	t.Helper()
	for _, part := range []string{"diff --git a/tracked.txt b/tracked.txt", "--- a/tracked.txt\n+++ b/tracked.txt", "@@ -1,3 +1,3 @@", "-BEFORE", "+ACTUAL_AFTER", " KEEP", " TAIL",
		"diff --git a/created.txt b/created.txt", "--- /dev/null\n+++ b/created.txt", "@@ -0,0 +1,1 @@", "+ACTUAL_CREATED"} {
		require.Contains(t, diff, part, "最终 diff 必须包含真实磁盘变更的路径与 hunk")
	}
	paths := []string{}
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			paths = append(paths, line)
		}
	}
	require.ElementsMatch(t, []string{"diff --git a/tracked.txt b/tracked.txt", "diff --git a/created.txt b/created.txt"}, paths,
		"diff 不得纳入项目之外的 runtime HOME 或凭据")
}

func verifyClaudeEventGapHistory(t *testing.T, history claudeEventTurn, completed map[string]json.RawMessage, scenario, cwd string) {
	t.Helper()
	paths, texts := []string{}, []string{}
	for _, raw := range history.Items {
		var item struct {
			ID, Type, Text, Status string
			Changes                []struct{ Path, Diff string }
		}
		require.NoError(t, json.Unmarshal(raw, &item))
		require.NotContains(t, string(raw), claudeUsageWarning, "原生 warning 不得存入助手 Markdown、推理或工具历史")
		if item.Type != "fileChange" && item.Type != "agentMessage" {
			continue
		}
		require.Contains(t, completed, item.ID, "历史必须来自本次真实终态 Item")
		require.JSONEq(t, string(completed[item.ID]), string(raw), "历史和客户端收到的完整 Item 必须一致")
		if item.Type == "agentMessage" {
			texts = append(texts, item.Text)
			continue
		}
		if scenario != "mutate" {
			continue
		}
		require.Equal(t, "completed", item.Status)
		for _, change := range item.Changes {
			paths = append(paths, change.Path)
			if change.Path == filepath.Join(cwd, "tracked.txt") {
				require.Contains(t, change.Diff, "-BEFORE")
				require.Contains(t, change.Diff, "+ACTUAL_AFTER")
			} else {
				require.Equal(t, filepath.Join(cwd, "created.txt"), change.Path)
				require.Contains(t, change.Diff, "+ACTUAL_CREATED")
			}
		}
	}
	require.Equal(t, []string{"EVENTS_010_" + scenario + "_DONE"}, texts, "助手文本必须只来自模型文本输出")
	if scenario == "mutate" {
		require.ElementsMatch(t, []string{filepath.Join(cwd, "tracked.txt"), filepath.Join(cwd, "created.txt")}, paths)
	}
}
