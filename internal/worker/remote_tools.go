package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/ports"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

func (p *Processor) handleRemoteGitHubTool(ctx context.Context,
	task *workerprotocol.Task, _ string, workspace ports.Workspace, branch string,
	request codex.ToolCallRequest, report func(string, json.RawMessage),
) (codex.ToolCallResult, error) {
	namespace := ""
	if request.Namespace != nil {
		namespace = *request.Namespace
	}
	if namespace == "github" || namespace == "tyrs_hand" {
		result, err := p.client.CallTool(ctx, task, request)
		report("dynamic_tool.finished", remoteEventPayload(map[string]any{
			"namespace": namespace, "tool": request.Tool, "callId": request.CallID,
			"success": err == nil && result.Success, "error": trimError(err),
		}))
		return result, err
	}
	if namespace == browserToolNamespace {
		result, err := executeBrowserTool(ctx, p.cfg, task.Claimed.ID.String(),
			workspace.WorktreePath, request)
		report("local_tool.finished", remoteEventPayload(map[string]any{
			"namespace": namespace, "tool": request.Tool, "callId": request.CallID,
			"success": err == nil && result.Success, "error": trimError(err),
		}))
		return result, err
	}
	if namespace != "git" {
		return codex.ToolCallResult{}, errors.New("未知 dynamic tool namespace")
	}
	result, err := p.executeRemoteGitTool(ctx, task, workspace, branch, request)
	report("local_tool.finished", remoteEventPayload(map[string]any{
		"namespace": namespace, "tool": request.Tool, "callId": request.CallID,
		"success": err == nil && result.Success, "error": trimError(err),
	}))
	return result, err
}

func (p *Processor) handleRemoteHostDiscordTool(ctx context.Context,
	task *workerprotocol.Task, runtime hostWorkspaceRuntime, request codex.ToolCallRequest,
	report func(string, json.RawMessage),
) (codex.ToolCallResult, error) {
	namespace := ""
	if request.Namespace != nil {
		namespace = *request.Namespace
	}
	var result codex.ToolCallResult
	var err error
	switch namespace {
	case browserToolNamespace:
		result, err = executeBrowserTool(ctx, p.cfg, task.Claimed.ID.String(),
			runtime.Workspace, request)
	case "git":
		result, err = p.executeRemoteHostGit(ctx, runtime, request)
	default:
		err = errors.New("未知 dynamic tool namespace")
	}
	report("discord.tool", remoteEventPayload(map[string]any{"namespace": namespace,
		"tool": request.Tool, "callId": request.CallID,
		"success": err == nil && result.Success, "error": trimError(err)}))
	return result, err
}

func (p *Processor) executeRemoteHostGit(ctx context.Context,
	runtime hostWorkspaceRuntime,
	request codex.ToolCallRequest,
) (codex.ToolCallResult, error) {
	if request.ThreadID == "" || request.TurnID == "" || request.CallID == "" {
		return codex.ToolCallResult{}, errors.New("本地 Tool Call 缺少 thread、turn 或 call ID")
	}
	if runtime.ProjectKind != "git" {
		return codex.ToolCallResult{}, errors.New("当前项目不是 Git 仓库")
	}
	switch request.Tool {
	case "status":
		status, err := runHostGit(ctx, runtime.Workspace, "status", "--porcelain=v1", "--branch")
		return codex.TextToolResult(status, err == nil), err
	case "commit":
		var arguments struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(request.Arguments, &arguments); err != nil {
			return codex.ToolCallResult{}, err
		}
		if strings.TrimSpace(arguments.Message) == "" {
			return codex.ToolCallResult{}, errors.New("提交信息不能为空")
		}
		if _, err := runHostGit(ctx, runtime.Workspace, "add", "--all"); err != nil {
			return codex.ToolCallResult{}, err
		}
		if _, err := runHostGit(ctx, runtime.Workspace, "commit", "-m", arguments.Message); err != nil {
			return codex.ToolCallResult{}, err
		}
		sha, err := runHostGit(ctx, runtime.Workspace, "rev-parse", "HEAD")
		return codex.TextToolResult(fmt.Sprintf(`{"sha":%q}`, strings.TrimSpace(sha)),
			err == nil), err
	case "publish_branch":
		if runtime.RemoteURL == "" {
			return codex.ToolCallResult{}, errors.New("当前项目没有远端，不能发布分支")
		}
		branch, err := runHostGit(ctx, runtime.Workspace, "branch", "--show-current")
		branch = strings.TrimSpace(branch)
		if err == nil && branch == "" {
			err = errors.New("当前 Git 工作区处于 detached HEAD")
		}
		if err == nil {
			_, err = runHostGit(ctx, runtime.Workspace, "push", "--set-upstream", "origin", "HEAD")
		}
		sha := ""
		if err == nil {
			sha, err = runHostGit(ctx, runtime.Workspace, "rev-parse", "HEAD")
		}
		return codex.TextToolResult(fmt.Sprintf(`{"branch":%q,"sha":%q}`, branch, sha),
			err == nil), err
	default:
		return codex.ToolCallResult{}, fmt.Errorf("本地 Git 工具 %s 未授权", request.Tool)
	}
}

func runHostGit(ctx context.Context, workspace string, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", workspace}, arguments...)...)
	output, err := command.CombinedOutput()
	value := strings.TrimSpace(string(output))
	if err != nil {
		if value == "" {
			return "", err
		}
		return "", fmt.Errorf("git %s: %s: %w", strings.Join(arguments, " "), value, err)
	}
	return value, nil
}

func (p *Processor) executeRemoteGitTool(ctx context.Context, task *workerprotocol.Task,
	workspace ports.Workspace, branch string, request codex.ToolCallRequest,
) (codex.ToolCallResult, error) {
	if request.ThreadID == "" || request.TurnID == "" || request.CallID == "" {
		return codex.ToolCallResult{}, errors.New("本地 Tool Call 缺少 thread、turn 或 call ID")
	}
	switch request.Tool {
	case "status":
		status, err := p.workspace.Status(ctx, workspace.WorktreePath)
		return codex.TextToolResult(status, err == nil), err
	case "commit":
		var arguments struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(request.Arguments, &arguments); err != nil {
			return codex.ToolCallResult{}, err
		}
		sha, err := p.workspace.Commit(ctx, workspace.WorktreePath, arguments.Message)
		if err == nil {
			err = p.client.WorkspaceState(ctx, task, workerprotocol.WorkspaceState{
				CachePath: workspace.CachePath, WorktreePath: workspace.WorktreePath,
				Branch: workspace.Branch, HeadSHA: sha, Status: "ready",
			})
		}
		return codex.TextToolResult(fmt.Sprintf(`{"sha":%q}`, sha), err == nil), err
	case "publish_branch":
		credential, err := p.client.GitCredential(ctx, task, "push",
			request.ThreadID, request.TurnID)
		if err != nil {
			return codex.ToolCallResult{}, err
		}
		sha, err := p.workspace.Publish(ctx, workspace.WorktreePath, branch, credential)
		if err == nil {
			err = p.client.WorkspaceState(ctx, task, workerprotocol.WorkspaceState{
				CachePath: workspace.CachePath, WorktreePath: workspace.WorktreePath,
				Branch: workspace.Branch, HeadSHA: sha, Status: "ready",
			})
		}
		return codex.TextToolResult(fmt.Sprintf(`{"branch":%q,"sha":%q}`, branch, sha),
			err == nil), err
	default:
		return codex.ToolCallResult{}, fmt.Errorf("本地 Git 工具 %s 未授权", request.Tool)
	}
}
