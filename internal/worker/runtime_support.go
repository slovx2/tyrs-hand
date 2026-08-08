package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/ports"
	"github.com/slovx2/tyrs-hand/internal/replygate"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

var errDiscordTurnStopped = errors.New("当前 Discord Codex Turn 已被停止")

const (
	turnCleanupTimeout        = 5 * time.Second
	workerCodexSandbox        = "danger-full-access"
	workerCodexApprovalPolicy = "never"
)

func workerThreadOptions(options ports.ThreadOptions) ports.ThreadOptions {
	options.Sandbox = workerCodexSandbox
	options.ApprovalPolicy = workerCodexApprovalPolicy
	return options
}

func needsCleanupInterrupt(err error) bool {
	if err == nil || errors.Is(err, errDiscordTurnStopped) {
		return false
	}
	var codexErr *workerprotocol.CodexTurnError
	return !errors.As(err, &codexErr) || codexErr.WillRetry
}

func interruptTurnBestEffort(runtime *codex.Runtime, threadID, turnID string) {
	ctx, cancel := context.WithTimeout(context.Background(), turnCleanupTimeout)
	defer cancel()
	_ = runtime.InterruptTurn(ctx, threadID, turnID)
}

func resolveSkills(worktree string, names []string) ([]ports.SkillRef, error) {
	result := make([]ports.SkillRef, 0, len(names))
	for _, name := range names {
		if name == "" || strings.ContainsAny(name, `/\\`) {
			return nil, fmt.Errorf("仓库 Skill 名称 %q 无效", name)
		}
		path, err := filepath.Abs(filepath.Join(worktree, ".agents", "skills", name, "SKILL.md"))
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("仓库 Skill %s 不存在: %w", name, err)
		}
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			path = resolved
		}
		result = append(result, ports.SkillRef{Name: name, Path: path})
	}
	return result, nil
}

func localGitSpec(allowPublish bool) ports.DynamicToolSpec {
	result := ports.DynamicToolSpec{Type: "namespace", Name: "git",
		Description: "Inspect and publish the current managed Git workspace.",
		Tools: []ports.DynamicToolSpec{
			{Type: "function", Name: "status", Description: "Read the current worktree status.", InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)},
			{Type: "function", Name: "commit", Description: "Stage all current worktree changes and create a commit.", InputSchema: json.RawMessage(`{"type":"object","properties":{"message":{"type":"string","minLength":1,"maxLength":200}},"required":["message"],"additionalProperties":false}`)},
		}}
	if allowPublish {
		result.Tools = append(result.Tools, ports.DynamicToolSpec{Type: "function",
			Name: "publish_branch", Description: "Push the current HEAD to its managed GitHub branch.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)})
	}
	return result
}

func githubReplySpec() ports.DynamicToolSpec {
	return ports.DynamicToolSpec{Type: "namespace", Name: "tyrs_hand",
		Description: "Send the required final reply through the platform.",
		Tools: []ports.DynamicToolSpec{{Type: "function", Name: "reply_to_github",
			Description: "Post the one final user-facing reply to the current authorized GitHub issue or pull request.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"body":{"type":"string","minLength":1,"maxLength":60000}},"required":["body"],"additionalProperties":false}`)}}}
}

func applyBrowserMCPConfig(runtimeConfig map[string]any, cfg config.Config,
	tokenEnvironment string, taskIDs ...string,
) {
	if cfg.BrowserMCPURL == "" {
		return
	}
	servers, _ := runtimeConfig["mcp_servers"].(map[string]any)
	if servers == nil {
		servers = make(map[string]any)
	}
	browser := map[string]any{"url": cfg.BrowserMCPURL,
		"bearer_token_env_var": tokenEnvironment, "startup_timeout_sec": 10.0,
		"tool_timeout_sec": 120.0, "required": false,
		"default_tools_approval_mode": "approve"}
	if len(taskIDs) > 0 && taskIDs[0] != "" {
		browser["http_headers"] = map[string]string{"X-Tyrs-Browser-Task-Id": taskIDs[0]}
	}
	servers["chrome"] = browser
	runtimeConfig["mcp_servers"] = servers
}

func hideManagedSecrets(config map[string]any) {
	policy, _ := config["shell_environment_policy"].(map[string]any)
	if policy == nil {
		policy = map[string]any{"inherit": "all"}
	}
	if values, ok := policy["set"].(map[string]any); ok {
		delete(values, codex.BrowserMCPWorkerTokenEnvironment)
		delete(values, codex.BrowserMCPDesktopTokenEnvironment)
	}
	excluded := make([]string, 0, 4)
	switch values := policy["exclude"].(type) {
	case []string:
		excluded = append(excluded, values...)
	case []any:
		for _, value := range values {
			if name, ok := value.(string); ok {
				excluded = append(excluded, name)
			}
		}
	}
	policy["exclude"] = appendUniqueStrings(excluded,
		codex.BrowserMCPWorkerTokenEnvironment,
		codex.BrowserMCPDesktopTokenEnvironment)
	config["shell_environment_policy"] = policy
}

func appendUniqueStrings(values []string, additions ...string) []string {
	seen := make(map[string]struct{}, len(values)+len(additions))
	result := make([]string, 0, len(values)+len(additions))
	for _, value := range append(values, additions...) {
		if _, exists := seen[value]; value == "" || exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func prepareCodexRuntime(workerDataRoot string, cfg config.Config,
	taskIDs ...string,
) map[string]any {
	runtimeConfig := replygate.SessionConfig()
	runtimeConfig["shell_environment_policy"] = map[string]any{"inherit": "all"}
	if workerDataRoot != "" {
		runtimeConfig["sandbox_workspace_write"] = map[string]any{"writable_roots": []string{
			filepath.Join(workerDataRoot, "caches"), filepath.Join(workerDataRoot, "state")}}
	}
	applyBrowserMCPConfig(runtimeConfig, cfg, codex.BrowserMCPWorkerTokenEnvironment,
		taskIDs...)
	hideManagedSecrets(runtimeConfig)
	return runtimeConfig
}

func browserDeveloperInstructions(_ config.Config, current string) string { return current }

type jobContext struct {
	Owner, Repository, Kind, HTMLURL string
	HeadRepository, HeadRef, HeadSHA string
	BaseRef, BaseSHA                 string
	Number                           int
}

func githubWorkItemAdditionalContext(job jobContext,
	workspace ports.Workspace,
) map[string]ports.AdditionalContextEntry {
	url := job.HTMLURL
	if url == "" {
		path := "issues"
		if job.Kind == "pull_request" {
			path = "pull"
		}
		url = fmt.Sprintf("https://github.com/%s/%s/%s/%d", job.Owner, job.Repository,
			path, job.Number)
	}
	payload := map[string]any{"provider": "github",
		"repository": job.Owner + "/" + job.Repository, "kind": job.Kind,
		"number": job.Number, "url": url,
		"workspace": map[string]any{"branch": workspace.Branch,
			"policy": "temporary_lightweight"}}
	if job.Kind == "pull_request" {
		payload["pullRequest"] = map[string]any{"sourceRepository": job.HeadRepository,
			"sourceBranch": job.HeadRef, "sourceSha": job.HeadSHA,
			"targetBranch": job.BaseRef, "targetSha": job.BaseSHA,
			"fetchedRef": fmt.Sprintf("refs/remotes/pull/%d", job.Number)}
	}
	encoded, _ := json.Marshal(payload)
	return map[string]ports.AdditionalContextEntry{"github_work_item": {
		Kind: "application", Value: string(encoded)}}
}

func completedTurn(raw json.RawMessage, threadID, turnID string) (bool, string) {
	var payload struct {
		ThreadID string `json:"threadId"`
		Turn     struct {
			ID, Status string
		} `json:"turn"`
	}
	if json.Unmarshal(raw, &payload) != nil || payload.ThreadID != threadID ||
		payload.Turn.ID != turnID {
		return false, ""
	}
	return true, payload.Turn.Status
}

func isActiveCodexTurnStatus(status string) bool {
	return status == "inProgress" || status == "active" || status == "running"
}

func eventBelongsToTurn(raw json.RawMessage, threadID, turnID, clientID string) bool {
	var payload struct {
		ThreadID, TurnID string
		Turn             struct {
			ID, ClientUserMessageID string
		} `json:"turn"`
	}
	if json.Unmarshal(raw, &payload) != nil || payload.ThreadID != threadID {
		return false
	}
	eventTurn := payload.Turn.ID
	if eventTurn == "" {
		eventTurn = payload.TurnID
	}
	return eventTurn == turnID || clientID != "" && payload.Turn.ClientUserMessageID == clientID
}

func eventTurnID(raw json.RawMessage) string {
	var payload struct {
		TurnID string `json:"turnId"`
		Turn   struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	_ = json.Unmarshal(raw, &payload)
	if payload.Turn.ID != "" {
		return payload.Turn.ID
	}
	return payload.TurnID
}

func finalOutputFromEvent(event codex.Event) (string, string) {
	if event.Method != "item/completed" {
		return "", ""
	}
	var payload struct {
		Item struct{ Type, Phase, Text string } `json:"item"`
	}
	if json.Unmarshal(event.Params, &payload) != nil {
		return "", ""
	}
	if payload.Item.Type == "plan" {
		return strings.TrimSpace(payload.Item.Text), "plan"
	}
	if payload.Item.Type == "agentMessage" &&
		(payload.Item.Phase == "final_answer" || payload.Item.Phase == "") {
		return strings.TrimSpace(payload.Item.Text), "agentMessage"
	}
	return "", ""
}

func finalAnswerDelta(event codex.Event) string {
	if event.Method != "item/agentMessage/delta" && event.Method != "item/delta" {
		return ""
	}
	var payload struct{ Delta, Text string }
	if json.Unmarshal(event.Params, &payload) != nil {
		return ""
	}
	if payload.Delta != "" {
		return payload.Delta
	}
	return payload.Text
}

func resetTimer(timer *time.Timer, duration time.Duration) {
	stopTimer(timer)
	timer.Reset(duration)
}

func stopTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func shortID(id uuid.UUID) string { return strings.ReplaceAll(id.String()[:8], "-", "") }

func withoutGenericReply(tools []string) []string {
	result := make([]string, 0, len(tools))
	for _, tool := range tools {
		if tool != "add_issue_comment" {
			result = append(result, tool)
		}
	}
	return result
}
