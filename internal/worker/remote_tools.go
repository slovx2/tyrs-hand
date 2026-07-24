package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/devcontainer"
	"github.com/slovx2/tyrs-hand/internal/ports"
	"github.com/slovx2/tyrs-hand/internal/replygate"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

func (p *RemoteProcessor) handleRemoteGitHubTool(ctx context.Context,
	task *workerprotocol.Task, codexHome string, workspace ports.Workspace, branch string,
	request codex.ToolCallRequest, report func(string, json.RawMessage),
) (codex.ToolCallResult, error) {
	namespace := ""
	if request.Namespace != nil {
		namespace = *request.Namespace
	}
	if namespace == "github" || namespace == "tyrs_hand" {
		result, err := p.client.CallTool(ctx, task, request)
		if err == nil && namespace == "tyrs_hand" && request.Tool == "reply_to_github" {
			err = replygate.MarkDelivered(codexHome, request.ThreadID)
		}
		report("dynamic_tool.finished", remoteEventPayload(map[string]any{
			"namespace": namespace, "tool": request.Tool, "callId": request.CallID,
			"success": err == nil && result.Success, "error": trimError(err),
		}))
		return result, err
	}
	if namespace == browserToolNamespace {
		result, err := executeBrowserTool(ctx, p.cfg, task.Claimed.ID.String(),
			workspace.WorktreePath, nil, p.development, request)
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

func (p *RemoteProcessor) handleRemoteDiscordTool(ctx context.Context,
	task *workerprotocol.Task, runtime devcontainer.Runtime, request codex.ToolCallRequest,
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
			runtime.Workspace, &runtime, p.development, request)
	case "git":
		result, err = p.executeRemoteContainerGit(ctx, task, runtime, request)
	default:
		err = errors.New("未知 dynamic tool namespace")
	}
	report("discord.tool", remoteEventPayload(map[string]any{"namespace": namespace,
		"tool": request.Tool, "callId": request.CallID,
		"success": err == nil && result.Success, "error": trimError(err)}))
	return result, err
}

func (p *RemoteProcessor) executeRemoteContainerGit(ctx context.Context,
	task *workerprotocol.Task, runtime devcontainer.Runtime,
	request codex.ToolCallRequest,
) (codex.ToolCallResult, error) {
	if request.ThreadID == "" || request.TurnID == "" || request.CallID == "" {
		return codex.ToolCallResult{}, errors.New("本地 Tool Call 缺少 thread、turn 或 call ID")
	}
	switch request.Tool {
	case "status":
		status, err := p.development.Git(ctx, runtime, "status", "--porcelain=v1", "--branch")
		return codex.TextToolResult(status, err == nil), err
	case "commit":
		var arguments struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(request.Arguments, &arguments); err != nil {
			return codex.ToolCallResult{}, err
		}
		sha, err := p.development.Commit(ctx, runtime, arguments.Message)
		return codex.TextToolResult(fmt.Sprintf(`{"sha":%q}`, strings.TrimSpace(sha)),
			err == nil), err
	case "publish_branch":
		credential, err := p.client.GitCredential(ctx, task, "push",
			request.ThreadID, request.TurnID)
		if err != nil {
			return codex.ToolCallResult{}, err
		}
		branch, sha, err := p.development.Publish(ctx, runtime, credential)
		return codex.TextToolResult(fmt.Sprintf(`{"branch":%q,"sha":%q}`, branch, sha),
			err == nil), err
	default:
		return codex.ToolCallResult{}, fmt.Errorf("本地 Git 工具 %s 未授权", request.Tool)
	}
}

func (p *RemoteProcessor) executeRemoteGitTool(ctx context.Context, task *workerprotocol.Task,
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
