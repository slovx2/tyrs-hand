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
	scheduledTaskActorLogin   = "tyrs-hand-scheduler"
)

const scheduledTaskDeveloperInstruction = "这是无人值守定时任务。尽量根据现有上下文推断，" +
	"避免请求额外输入；确实无法继续时可使用正常交互机制。"

func workerThreadOptions(options ports.ThreadOptions) ports.ThreadOptions {
	options.Sandbox = workerCodexSandbox
	options.ApprovalPolicy = workerCodexApprovalPolicy
	return options
}

func workspaceDeveloperInstructions(task *workerprotocol.Task, current string) string {
	current = strings.TrimSpace(current)
	if task == nil || task.Claimed.ActorLogin != scheduledTaskActorLogin {
		return current
	}
	if current == "" {
		return scheduledTaskDeveloperInstruction
	}
	return current + "\n\n" + scheduledTaskDeveloperInstruction
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

func resolveWorkspaceSkills(workspace string, names []string) ([]ports.SkillRef, error) {
	return resolveSkills(workspace, names)
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

func githubWorkItemTools(cfg config.Config, githubSpec ports.DynamicToolSpec) []ports.DynamicToolSpec {
	return withBrowserTools(cfg, githubSpec, localGitSpec(true), githubReplySpec())
}

func automationSpec() ports.DynamicToolSpec {
	return ports.DynamicToolSpec{Type: "namespace", Name: "tyrs_hand",
		Description: "Manage scheduled tasks scoped to the current Tyrs Hand workspace.",
		Tools: []ports.DynamicToolSpec{{Type: "function", Name: "automation_update",
			Description: "Create, update, list, delete, or immediately run a scheduled task. " +
				"Create heartbeat tasks by default so execution continues in the current session. " +
				"Choose standalone only when the user explicitly asks for an independent/new session or project-level task. " +
				"Standalone tasks target the current project; heartbeat tasks target the current session. " +
				"Schedules are RFC 5545 recurrence sets with DTSTART and optional RRULE/RDATE/EXDATE.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"action":{"type":"string","enum":["create","update","list","delete","run_now"]},
					"task_id":{"type":"string","format":"uuid"},
					"kind":{"type":"string","enum":["standalone","heartbeat"],"default":"heartbeat","description":"Defaults to heartbeat. Use standalone only when the user explicitly requests an independent/new session or project-level task."},
					"name":{"type":"string","minLength":1,"maxLength":120},
					"prompt":{"type":"string","minLength":1,"maxLength":100000},
					"schedule":{"type":"string","minLength":1,"maxLength":65536},
					"timezone":{"type":"string","minLength":1,"maxLength":128},
					"status":{"type":"string","enum":["active","paused"]},
					"settings":{"type":"object","additionalProperties":false,"properties":{
						"agent_profile_id":{"type":"string","format":"uuid"},
						"model":{"type":"string","maxLength":128},
						"reasoning_effort":{"type":"string","maxLength":64},
						"service_tier":{"type":"string","enum":["standard","fast"]}
					}},
					"include_deleted":{"type":"boolean"}
				},
				"required":["action"],
				"additionalProperties":false
			}`)}}}
}

func isWorkerLiveVoiceTool(name string) bool {
	switch name {
	case "list_sessions", "create_session", "send_message", "read_session", "transfer_voice_call", "end_voice_call":
		return true
	default:
		return false
	}
}

// Copied from ChatGPT.app 26.908.40834 H2n/U2n/l2n. Tools mapped to tyrs_hand.
// First period omits capture_screen_context, wait_threads, create_thread, and app shopping.
const liveVoiceDeveloperInstruction = `Realtime voice is active for this existing Codex task. Preserve the task's original instructions, role, collaboration mode, permissions, memory policy, and ongoing work.

Every ordinary spoken or frontend-context response must begin at byte zero with [STATUS] followed by one ASCII space for meaningful progress, or [COMPLETE] followed by one ASCII space for a final result, question, or blocker. [COMMENTARY] is also accepted as progress, and [ANALYSIS] remains silent context. Never speak or repeat a channel prefix.

To display exact Markdown, links, images, code, or other visual content, begin at byte zero with the bare directive ::codex-realtime-inline{}, followed by a newline and the Markdown. Do not put a channel tag before the directive.

During this voice session, these tools are deferred: tyrs_hand.transfer_voice_call and tyrs_hand.end_voice_call. Load and use them only when needed for this active session. End the voice call only when the user clearly intends to end the call; a request to stop work, stop speaking, or pause is not sufficient.

Use tyrs_hand.list_sessions to resolve which session the user means. Use tyrs_hand.create_session only on the current worker. Use tyrs_hand.send_message to follow up another session. Use tyrs_hand.read_session for a compact status. Use tyrs_hand.transfer_voice_call only after you know the session id.`

const liveVoiceEndInstruction = `Realtime voice mode has ended. Resume this task's original instructions, role, collaboration mode, normal text-output policy, permissions, memory policy, and ongoing work. Do not add realtime channel prefixes or the ::codex-realtime-inline{} directive. Do not call tyrs_hand.end_voice_call or tyrs_hand.transfer_voice_call for the ended session; they apply only after another explicit voice session begins.`

const liveVoiceCoordinatorInstruction = `You are coordinating a voice chat.

Your job is to keep the live conversation responsive while helping the user get work done. Think with the user in this session, and use other workspace sessions on this worker for slow or independent work.

Do not dispatch work just because a request uses tools or touches a project. Also do not keep blocking work here just because the final decision is interactive.

Choose one of three modes:

1. Converse here.
Use this session for brainstorming, prioritizing, clarifying, quick advice, lightweight planning, and interactive decision support. Stay here when the user is trying to think with you or build shared context.

2. Quick check here.
Use this session for small, fast checks when the result immediately helps the live conversation. Examples: listing sessions, checking the current branch, doing a quick pass over today's open PRs to help choose one, reading a short status, or answering "what do you think?"

3. Delegate blocking mechanics.
Use tyrs_hand.create_session and tyrs_hand.send_message for slow or multi-step work, especially implementation, deep repo investigation, log collection, drafting, monitoring, or tasks that can proceed independently. If the task needs user choices, have the worker session gather options and report back; keep the choice and confirmation in this coordinator session.

When dispatching:
- Call tyrs_hand.list_sessions first. Only sessions on the current worker are allowed.
- tyrs_hand.create_session must stay on this worker; optional projectId must belong here.
- For existing session work, use tyrs_hand.list_sessions and tyrs_hand.send_message to find or steer the relevant session. Prefer tyrs_hand.read_session for a compact status over repeating a long follow-up.
- Use tyrs_hand.transfer_voice_call only after you know the session id.
- Use tyrs_hand.end_voice_call only when the user clearly intends to end the voice call; stopping work, stopping speech, or pausing is not sufficient.
- Every delegated prompt must include a return-report instruction. Tell the worker: "When you finish or get blocked, send a short message back to this coordinator session. Include the outcome, current status, and any decision needed from the user."
- Treat the return report as part of the worker's task, not optional follow-up.

Examples:
- "What should we do today?" Stay here.
- "Look at my open PRs from today and help me pick one." Do a quick pass here unless it turns into deep investigation.
- "Implement the fix in that PR." Dispatch to a project worker session.

Every ordinary spoken or frontend-context response must begin at byte zero with [STATUS] followed by one ASCII space for meaningful progress, or [COMPLETE] followed by one ASCII space for a final result, question, or blocker. [COMMENTARY] is also accepted as progress, and [ANALYSIS] remains silent context. Never speak or repeat a channel prefix.

To display exact Markdown, links, images, code, or other visual content, begin at byte zero with the bare directive ::codex-realtime-inline{}, followed by a newline and the Markdown. Do not put a channel tag before the directive.

If unsure, start with a brief answer or clarifying question here. Dispatch once the work becomes mostly waiting, gathering, executing, or otherwise blocking the live conversation.`

func applyLiveVoiceSessionSupport(snapshot *workerprotocol.SessionSnapshot,
	developerInstructions string, tools []ports.DynamicToolSpec,
) (string, []ports.DynamicToolSpec) {
	if snapshot == nil {
		return developerInstructions, tools
	}
	switch {
	case snapshot.VoiceBound && snapshot.VoiceCoordinator:
		developerInstructions = appendDeveloperInstruction(developerInstructions, liveVoiceCoordinatorInstruction)
		tools = mergeVoiceControlTools(tools)
	case snapshot.VoiceBound:
		developerInstructions = appendDeveloperInstruction(developerInstructions, liveVoiceDeveloperInstruction)
		tools = mergeVoiceControlTools(tools)
	case snapshot.VoiceEnded:
		developerInstructions = appendDeveloperInstruction(developerInstructions, liveVoiceEndInstruction)
	}
	return developerInstructions, tools
}

func appendDeveloperInstruction(current, extra string) string {
	current = strings.TrimSpace(current)
	extra = strings.TrimSpace(extra)
	if extra == "" {
		return current
	}
	if current == "" {
		return extra
	}
	return current + "\n\n" + extra
}

func mergeVoiceControlTools(tools []ports.DynamicToolSpec) []ports.DynamicToolSpec {
	extra := voiceControlSpec().Tools
	for i, spec := range tools {
		if spec.Type == "namespace" && spec.Name == "tyrs_hand" {
			merged := make([]ports.DynamicToolSpec, 0, len(spec.Tools)+len(extra))
			merged = append(merged, spec.Tools...)
			merged = append(merged, extra...)
			tools[i].Tools = merged
			return tools
		}
	}
	return append(tools, voiceControlSpec())
}

func voiceControlSpec() ports.DynamicToolSpec {
	return ports.DynamicToolSpec{Type: "namespace", Name: "tyrs_hand",
		Description: "Control Tyrs Hand workspace sessions from an active voice call on this worker.",
		Tools: []ports.DynamicToolSpec{
			{Type: "function", Name: "list_sessions", Description: "List sessions on the current worker only.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)},
			{Type: "function", Name: "create_session", Description: "Create a session on the current worker.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"},"projectId":{"type":"string","format":"uuid"}},"additionalProperties":false}`)},
			{Type: "function", Name: "send_message", Description: "Send a follow-up to a session on the current worker.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"sessionId":{"type":"string","format":"uuid"},"text":{"type":"string","minLength":1}},"required":["sessionId","text"],"additionalProperties":false}`)},
			{Type: "function", Name: "read_session", Description: "Read a session on the current worker.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"sessionId":{"type":"string","format":"uuid"}},"required":["sessionId"],"additionalProperties":false}`)},
			{Type: "function", Name: "transfer_voice_call", Description: "Move the voice call to another session on this worker.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"sessionId":{"type":"string","format":"uuid"}},"required":["sessionId"],"additionalProperties":false}`)},
			{Type: "function", Name: "end_voice_call", Description: "End the voice call only when the user clearly wants to hang up.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)},
		}}
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
