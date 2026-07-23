package discordintegration

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestConversationActionTrackerPackagesAndUpdatesActions(t *testing.T) {
	tracker := NewConversationActionTracker(time.Now().Add(-87 * time.Second))
	started := progressEvent(t, map[string]any{
		"id": "command-1", "type": "commandExecution", "status": "inProgress",
		"command": "/bin/zsh -lc 'sed -n 1,80p /Volumes/workspace/tyrs-hand/internal/codex/client.go'",
		"commandActions": []any{map[string]any{
			"type": "read", "path": "/Volumes/workspace/tyrs-hand/internal/codex/client.go",
		}},
	})
	require.True(t, tracker.ApplyEvent("item/started", started))
	require.False(t, tracker.ApplyEvent("item/started", started))

	completed := progressEvent(t, map[string]any{
		"id": "command-1", "type": "commandExecution", "status": "completed",
		"commandActions": []any{map[string]any{
			"type": "read", "path": "/Volumes/workspace/tyrs-hand/internal/codex/client.go",
		}},
		"aggregatedOutput": "不应显示的命令结果",
	})
	require.True(t, tracker.ApplyEvent("item/completed", completed))

	mcp := progressEvent(t, map[string]any{
		"id": "tool-1", "type": "mcpToolCall", "status": "failed",
		"server": "github", "tool": "issue_read",
		"arguments": map[string]any{
			"repo": "tyrs-hand", "number": 12, "api_key": "sk_abcdefghijklmnopqrstuv",
		},
		"result": map[string]any{"content": "不应显示的工具结果"},
		"error":  "不应显示的错误正文",
	})
	require.True(t, tracker.ApplyEvent("item/completed", mcp))
	require.False(t, tracker.ApplyEvent("item/agentMessage/delta", json.RawMessage(`{"delta":"过程说明"}`)))

	rendered := tracker.Render("正在处理请求。", 87*time.Second)
	require.Contains(t, rendered, "`1m 27s` · `2 项动态`")
	require.Contains(t, rendered, "> ↳ 已读取 `client.go`")
	require.Contains(t, rendered, "> ↳ 调用未成功，正在继续处理：`github.issue_read`")
	require.NotContains(t, rendered, "api_key")
	require.Contains(t, rendered, "`repo=tyrs-hand`")
	require.NotContains(t, rendered, "不应显示")
	require.NotContains(t, rendered, "/Volumes/workspace")
	require.NotContains(t, rendered, "sk_")
}

func TestConversationActionTrackerPackagesSearchCommand(t *testing.T) {
	tracker := NewConversationActionTracker(time.Now())
	event := progressEvent(t, map[string]any{
		"id": "search-command", "type": "commandExecution", "status": "completed",
		"command": `/bin/zsh -lc "rg -n 'waitTurn|ProjectConversationStatus' internal/worker"`,
	})
	require.True(t, tracker.ApplyEvent("item/completed", event))

	rendered := tracker.Render("", time.Second)
	require.Contains(t, rendered, "> ↳ 已搜索 `rg -n 'waitTurn|ProjectConversationStatus'")
	require.NotContains(t, rendered, "/bin/zsh -lc")
}

func TestConversationActionTrackerKeepsAllActionsInOrder(t *testing.T) {
	tracker := NewConversationActionTracker(time.Now())
	for index := 1; index <= 10; index++ {
		event := progressEvent(t, map[string]any{
			"id": fmt.Sprintf("search-%d", index), "type": "webSearch",
			"status": "completed", "query": fmt.Sprintf("第 %d 个查询", index),
		})
		require.True(t, tracker.ApplyEvent("item/completed", event))
	}

	timeline := tracker.Timeline("", time.Second)
	rendered := strings.Join(timeline.Pages, "\n")
	require.Contains(t, rendered, "第 1 个查询")
	require.Contains(t, rendered, "第 10 个查询")
	for _, line := range strings.Split(rendered, "\n") {
		require.LessOrEqual(t, displayLineWidth(strings.TrimPrefix(line, "> ↳ ")), conversationLineWidth+1)
	}
}

func TestConversationActionTrackerTruncatesOneLineWithoutBreakingCode(t *testing.T) {
	tracker := NewConversationActionTracker(time.Now())
	longCommand := "go test ./" + strings.Repeat("very-long-package/", 12)
	event := progressEvent(t, map[string]any{
		"id": "long-command", "type": "commandExecution", "status": "inProgress",
		"command": longCommand,
	})
	require.True(t, tracker.ApplyEvent("item/started", event))

	rendered := tracker.Render("", time.Second)
	actionLine := strings.Split(rendered, "\n")[2]
	require.NotContains(t, actionLine, "\n")
	require.Contains(t, actionLine, "…")
	require.Zero(t, strings.Count(actionLine, "`")%2)
}

func TestConversationActionTrackerSeparatesCommentaryAndCompactsTools(t *testing.T) {
	tracker := NewConversationActionTracker(time.Now())
	require.True(t, tracker.ApplyEvent("item/started", progressEvent(t, map[string]any{
		"id": "comment-1", "type": "agentMessage", "phase": "commentary", "text": "先检查项目。",
	})))
	require.True(t, tracker.ApplyEvent("item/completed", progressEvent(t, map[string]any{
		"id": "tool-1", "type": "webSearch", "query": "Components V2",
	})))
	require.True(t, tracker.ApplyEvent("item/completed", progressEvent(t, map[string]any{
		"id": "tool-2", "type": "imageView", "path": "/tmp/example.png",
	})))
	require.True(t, tracker.ApplyEvent("item/completed", progressEvent(t, map[string]any{
		"id": "comment-2", "type": "agentMessage", "phase": "commentary", "text": "已确认交互结构。",
	})))
	rendered := strings.Join(tracker.Timeline("", time.Second).Pages, "\n")
	require.Contains(t, rendered, "先检查项目。\n\n> ↳ 已搜索网页")
	require.Contains(t, rendered, "\n> ↳ 已查看 `example.png`\n\n已确认交互结构。")
}

func TestConversationActionTrackerUpdatesCommentaryDeltaAndPaginatesLongText(t *testing.T) {
	tracker := NewConversationActionTracker(time.Now())
	require.True(t, tracker.ApplyEvent("item/started", progressEvent(t, map[string]any{
		"id": "comment-1", "type": "agentMessage", "phase": "commentary", "text": "",
	})))
	delta, err := json.Marshal(map[string]any{"itemId": "comment-1", "phase": "commentary",
		"delta": strings.Repeat("长内容", 1600)})
	require.NoError(t, err)
	require.True(t, tracker.ApplyEvent("item/agentMessage/delta", delta))
	timeline := tracker.Timeline("", time.Second)
	require.Greater(t, len(timeline.Pages), 1)
	for _, page := range timeline.Pages {
		require.LessOrEqual(t, len([]rune(page)), conversationPageBudget)
	}
	require.Equal(t, 1, timeline.Updates)
}

func TestConversationActionTrackerCountsUniqueCommentaryAndToolItems(t *testing.T) {
	tracker := NewConversationActionTracker(time.Now())
	require.True(t, tracker.ApplyEvent("item/started", progressEvent(t, map[string]any{
		"id": "comment-1", "type": "agentMessage", "phase": "commentary", "text": "",
	})))
	for index := 0; index < 100; index++ {
		delta, err := json.Marshal(map[string]any{
			"itemId": "comment-1", "phase": "commentary", "delta": "片段",
		})
		require.NoError(t, err)
		require.True(t, tracker.ApplyEvent("item/agentMessage/delta", delta))
	}
	started := progressEvent(t, map[string]any{
		"id": "tool-1", "type": "webSearch", "query": "relay protocol",
	})
	completed := progressEvent(t, map[string]any{
		"id": "tool-1", "type": "webSearch", "query": "relay protocol", "status": "completed",
	})
	require.True(t, tracker.ApplyEvent("item/started", started))
	require.True(t, tracker.ApplyEvent("item/completed", completed))
	require.False(t, tracker.ApplyEvent("item/completed", completed))

	timeline := tracker.Timeline("", time.Second)
	require.Equal(t, 2, timeline.Updates)
	require.Contains(t, tracker.Render("", time.Second), "`2 项动态`")
}

func TestConversationActionTrackerExcludesFinalAnswer(t *testing.T) {
	tracker := NewConversationActionTracker(time.Now())
	require.False(t, tracker.ApplyEvent("item/completed", progressEvent(t, map[string]any{
		"id": "answer", "type": "agentMessage", "phase": "final_answer", "text": "最终回答",
	})))
	require.NotContains(t, tracker.Render("处理中", time.Second), "最终回答")
}

func TestConversationActionTrackerDynamicToolDeduplicatesProtocolEvent(t *testing.T) {
	tracker := NewConversationActionTracker(time.Now())
	args := json.RawMessage(`{"owner":"slovx2","repo":"tyrs-hand"}`)
	require.True(t, tracker.ApplyDynamicTool("call-1", "github", "issue_read", args, "running"))
	require.False(t, tracker.ApplyEvent("item/started", progressEvent(t, map[string]any{
		"id": "call-1", "type": "dynamicToolCall", "status": "inProgress",
		"namespace": "github", "tool": "issue_read",
		"arguments": map[string]any{"owner": "slovx2", "repo": "tyrs-hand"},
	})))
	require.True(t, tracker.ApplyDynamicTool("call-1", "github", "issue_read", args, "completed"))
	rendered := tracker.Render("", time.Second)
	require.Equal(t, 1, strings.Count(rendered, "github.issue_read"))
	require.Contains(t, rendered, "已调用")
}

func TestConversationActionTrackerFormatsDelegationAndFileChanges(t *testing.T) {
	tracker := NewConversationActionTracker(time.Now())
	require.True(t, tracker.ApplyEvent("item/completed", progressEvent(t, map[string]any{
		"id": "collab-1", "type": "collabAgentToolCall", "status": "completed",
		"receiverThreadId": "fallback-agent", "arguments": map[string]any{"task_name": "reviewer"},
	})))
	require.True(t, tracker.ApplyEvent("item/completed", progressEvent(t, map[string]any{
		"id": "files-1", "type": "fileChange", "status": "completed",
		"changes": []any{
			map[string]any{"path": "/workspace/new.go", "kind": map[string]any{"type": "add"}},
			map[string]any{"path": "/workspace/old.go", "kind": map[string]any{"type": "delete"}},
		},
	})))
	require.True(t, tracker.ApplyEvent("item/completed", progressEvent(t, map[string]any{
		"id": "files-2", "type": "fileChange", "status": "completed",
		"changes": []any{map[string]any{"path": "/workspace/old.go", "kind": map[string]any{"type": "delete"}}},
	})))

	rendered := tracker.Render("", time.Second)
	require.Contains(t, rendered, "已委派 `reviewer`")
	require.Contains(t, rendered, "已创建 `new.go 等 2 个文件`")
	require.Contains(t, rendered, "已删除 `old.go`")
}

func progressEvent(t *testing.T, item map[string]any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{"item": item})
	require.NoError(t, err)
	return encoded
}
