package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/codexsettings"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/slovx2/tyrs-hand/internal/ports"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"go.uber.org/zap"
)

var remoteAttachmentName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func (p *RemoteProcessor) processRemoteDiscord(ctx context.Context, task *workerprotocol.Task,
	commands <-chan workerprotocol.RunCommand,
	report func(string, json.RawMessage),
) (workerprotocol.CompleteRequest, error) {
	snapshot := task.Snapshot.Development
	if snapshot == nil || snapshot.Development == nil {
		return workerprotocol.CompleteRequest{}, errors.New("development session 任务缺少开发环境快照")
	}
	if p.hostRuntime == nil {
		return workerprotocol.CompleteRequest{}, errors.New("宿主 Codex Runtime 尚未启动")
	}
	runtime, err := resolveHostDevelopmentRuntime(p.hostRuntime.WorkspaceRoot(),
		p.hostRuntime.CodexHome(), snapshot.Development)
	if err != nil {
		return workerprotocol.CompleteRequest{}, err
	}
	defer cleanupBrowserTask(p.cfg, task.Claimed.ID.String(), "worker")
	p.reportHostDevelopmentState(ctx, task, runtime, nil)
	defer p.reportHostDevelopmentState(context.Background(), task, runtime, nil)

	attachments, err := p.prepareRemoteAttachments(ctx, task, runtime)
	if err != nil {
		return workerprotocol.CompleteRequest{}, err
	}
	skills, err := resolveContainerSkills(runtime.Workspace, task.Claimed.Skills)
	if err != nil {
		return workerprotocol.CompleteRequest{}, err
	}
	_, runtimeConfig := prepareCodexRuntime(nil, "", p.cfg, "worker", task.Claimed.ID.String())
	client := p.hostRuntime.Client()
	codexRuntime := codex.NewRuntime(client)
	settings := task.Snapshot.Runtime
	developerInstructions := strings.TrimSpace(settings.GlobalAgents + "\n\n" +
		discordintegration.MultiplayerDeveloperInstructions)
	options := workerThreadOptions(ports.ThreadOptions{
		CWD: runtime.Workspace, Model: settings.Model,
		ReasoningEffort: settings.ReasoningEffort,
		ServiceTier:     codexsettings.RuntimeServiceTier(settings.ServiceTier),
		NetworkEnabled:  settings.NetworkEnabled,
		RuntimeConfig:   runtimeConfig,
		DeveloperInstructions: browserDeveloperInstructions(p.cfg,
			developerInstructions),
		DynamicTools: withBrowserTools(p.cfg, developmentGitTools(snapshot.Development)...),
	})
	if err := codexRuntime.ValidateSkills(ctx, runtime.Workspace, skills); err != nil {
		return workerprotocol.CompleteRequest{}, err
	}
	threadID, err := p.ensureRemoteThread(ctx, codexRuntime, task, options,
		p.hostRuntime.CodexHome(), report)
	if err != nil {
		return workerprotocol.CompleteRequest{}, err
	}
	unbind := p.hostRuntime.BindTool(threadID, func(toolCtx context.Context,
		request codex.ToolCallRequest,
	) (codex.ToolCallResult, error) {
		return p.handleRemoteHostDiscordTool(toolCtx, task, runtime, request, report)
	})
	defer unbind()
	unbindInteractive := p.hostRuntime.BindInteractive(threadID,
		func(inputCtx context.Context, request codex.ServerRequest) (any, error) {
			return p.handleRemoteInteractive(inputCtx, task, p.hostRuntime.Generation(), request)
		})
	defer unbindInteractive()
	subscription := client.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	defer subscription.Close()
	codexReport := remoteDiscordEventReporter(report)
	commandHandler := p.hostDiscordCommandHandler(task, runtime, skills, report)
	if task.Claimed.Recovering {
		result, recovered, recoverErr := p.reconcileRemoteTurn(ctx, codexRuntime,
			subscription.Events(), task, threadID, commands, commandHandler, codexReport)
		if recoverErr != nil {
			return workerprotocol.CompleteRequest{}, recoverErr
		}
		if recovered {
			return workerprotocol.CompleteRequest{Result: result}, nil
		}
	}
	input := remoteDevelopmentTurnInput(snapshot, task.Snapshot.Discord, runtime, attachments, skills)
	input.CollaborationMode = &ports.CollaborationMode{Mode: settings.CollaborationMode,
		Model: settings.Model, ReasoningEffort: settings.ReasoningEffort}
	turnID, err := codexRuntime.StartTurn(ctx, threadID, input)
	if err != nil {
		return workerprotocol.CompleteRequest{}, err
	}
	reportRuntimeSettingsApplied(report, task.Snapshot.Runtime, "turn/start")
	if err := p.client.RecordSubmission(ctx, task, turnID); err != nil {
		return workerprotocol.CompleteRequest{}, err
	}
	result, err := p.waitRemoteTurn(ctx, codexRuntime, subscription.Events(), task, threadID,
		turnID, commands, commandHandler, codexReport)
	if err != nil {
		if needsCleanupInterrupt(err) {
			interruptTurnBestEffort(codexRuntime, threadID, turnID)
		}
		return workerprotocol.CompleteRequest{}, err
	}
	return workerprotocol.CompleteRequest{Result: result}, nil
}

func resolveHostDevelopmentRuntime(workspaceRoot, codexHome string,
	spec *workerprotocol.DevelopmentSpec,
) (hostDevelopmentRuntime, error) {
	clean := filepath.Clean(filepath.FromSlash(strings.TrimSpace(spec.WorkspaceRelative)))
	parts := strings.Split(filepath.ToSlash(clean), "/")
	if len(parts) != 2 || parts[0] != "workspaces" || parts[1] == "" || parts[1] == "." ||
		parts[1] == ".." {
		return hostDevelopmentRuntime{}, errors.New("宿主工作区必须是 workspaces/<name>")
	}
	root, err := filepath.EvalSymlinks(workspaceRoot)
	if err != nil {
		return hostDevelopmentRuntime{}, fmt.Errorf("读取宿主工作区根目录: %w", err)
	}
	workspace, err := filepath.EvalSymlinks(filepath.Join(root, parts[1]))
	if err != nil {
		return hostDevelopmentRuntime{}, fmt.Errorf("宿主工作区 %s 不存在: %w", parts[1], err)
	}
	relative, err := filepath.Rel(root, workspace)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return hostDevelopmentRuntime{}, errors.New("宿主工作区不能逃逸固定工作区根目录")
	}
	info, err := os.Stat(workspace)
	if err != nil || !info.IsDir() {
		return hostDevelopmentRuntime{}, errors.New("宿主工作区不是目录")
	}
	return hostDevelopmentRuntime{Workspace: workspace, CodexHome: codexHome,
		ProjectKind: spec.WorkspaceKind, RemoteURL: spec.CloneURL}, nil
}

func developmentGitTools(spec *workerprotocol.DevelopmentSpec) []ports.DynamicToolSpec {
	if spec.WorkspaceKind != "git" {
		return nil
	}
	return []ports.DynamicToolSpec{localGitSpec(spec.CloneURL != "")}
}

func remoteDiscordEventReporter(report func(string, json.RawMessage)) func(string, json.RawMessage) {
	return func(eventType string, payload json.RawMessage) { report(eventType, payload) }
}

func (p *RemoteProcessor) reportHostDevelopmentState(ctx context.Context, task *workerprotocol.Task,
	runtime hostDevelopmentRuntime, cause error,
) {
	spec := *task.Snapshot.Development.Development
	state := workerprotocol.DevelopmentState{DevelopmentSpec: spec}
	if spec.WorkspaceKind == "git" {
		status, statusErr := runHostGit(ctx, runtime.Workspace, "status", "--porcelain=v1")
		head, headErr := runHostGit(ctx, runtime.Workspace, "rev-parse", "HEAD")
		state.WorkspaceDirty, state.WorkspaceHeadSHA = strings.TrimSpace(status) != "", head
		if statusErr != nil {
			cause = statusErr
		} else if headErr != nil {
			cause = headErr
		}
	}
	if cause != nil {
		state.Error = cause.Error()
	}
	requestCtx, cancel := context.WithTimeout(ctx, p.cfg.ControlTimeout)
	defer cancel()
	if err := p.client.DevelopmentState(requestCtx, task, state); err != nil {
		p.logger.Warn("回传 Discord 宿主工作区状态失败", zap.Error(err))
	}
}
