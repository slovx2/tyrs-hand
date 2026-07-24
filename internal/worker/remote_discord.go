package worker

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/codexsettings"
	"github.com/slovx2/tyrs-hand/internal/devcontainer"
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
	snapshot := task.Snapshot.Discord
	if snapshot == nil || snapshot.Development == nil {
		return workerprotocol.CompleteRequest{}, errors.New("discord 任务缺少开发环境快照")
	}
	if p.development == nil || !p.development.Enabled() {
		return workerprotocol.CompleteRequest{}, errors.New("discord 执行节点没有启用开发容器")
	}
	report("discord.progress", remoteEventPayload(map[string]string{
		"state": "running", "detail": "已接收消息，正在准备工作区。",
	}))
	fetchCredential, err := p.client.GitCredential(ctx, task, "fetch", "", "")
	if err != nil {
		return workerprotocol.CompleteRequest{}, err
	}
	defer cleanupBrowserTask(p.cfg, task.Claimed.ID.String())
	spec := remoteDevelopmentSpec(*snapshot.Development)
	runtime, state, err := p.development.EnsureRemote(ctx, spec, fetchCredential)
	p.reportDevelopmentState(ctx, task, state)
	if err != nil {
		return workerprotocol.CompleteRequest{}, err
	}
	defer func() {
		status, statusErr := p.development.Git(context.Background(), runtime,
			"status", "--porcelain=v1")
		head, headErr := p.development.Git(context.Background(), runtime, "rev-parse", "HEAD")
		state.WorkspaceDirty = strings.TrimSpace(status) != ""
		state.WorkspaceHeadSHA = strings.TrimSpace(head)
		if statusErr != nil {
			state.Error = statusErr.Error()
		} else if headErr != nil {
			state.Error = headErr.Error()
		}
		p.reportDevelopmentState(context.Background(), task, state)
	}()

	attachments, err := p.prepareRemoteAttachments(ctx, task, runtime)
	if err != nil {
		return workerprotocol.CompleteRequest{}, err
	}
	skills, err := resolveContainerSkills(runtime.Workspace, task.Claimed.Skills)
	if err != nil {
		return workerprotocol.CompleteRequest{}, err
	}
	_, runtimeConfig := prepareCodexRuntime(nil, "", p.cfg)
	environmentCodex, err := p.environments.ensure(runtime, nil)
	if err != nil {
		return workerprotocol.CompleteRequest{}, err
	}
	applyModelProviderConfig(runtimeConfig, environmentCodex.runtime.ModelSource,
		environmentCodex.runtime.ModelBaseURL)
	client := environmentCodex.client
	codexRuntime := codex.NewRuntime(client)
	settings := task.Snapshot.Runtime
	options := workerThreadOptions(ports.ThreadOptions{
		CWD: runtime.Workspace, Model: settings.Model,
		ReasoningEffort:       settings.ReasoningEffort,
		ServiceTier:           codexsettings.RuntimeServiceTier(settings.ServiceTier),
		NetworkEnabled:        settings.NetworkEnabled,
		RuntimeConfig:         runtimeConfig,
		DeveloperInstructions: browserDeveloperInstructions(p.cfg, discordintegration.MultiplayerDeveloperInstructions),
		DynamicTools:          withBrowserTools(p.cfg, localGitSpec()),
	})
	if err := codexRuntime.ValidateSkills(ctx, runtime.Workspace, skills); err != nil {
		return workerprotocol.CompleteRequest{}, err
	}
	threadID, err := p.ensureRemoteThread(ctx, codexRuntime, task, options, runtime.CodexHome)
	if err != nil {
		return workerprotocol.CompleteRequest{}, err
	}
	unbind := environmentCodex.bindTool(threadID, func(toolCtx context.Context,
		request codex.ToolCallRequest,
	) (codex.ToolCallResult, error) {
		return p.handleRemoteDiscordTool(toolCtx, task, runtime, request, report)
	})
	defer unbind()
	unbindInteractive := environmentCodex.bindInteractive(threadID,
		func(inputCtx context.Context, request codex.ServerRequest) (any, error) {
			return p.handleRemoteInteractive(inputCtx, task, environmentCodex.generation, request)
		})
	defer unbindInteractive()
	subscription := client.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	defer subscription.Close()
	codexReport := remoteDiscordEventReporter(report)
	if task.Claimed.Recovering {
		result, recovered, recoverErr := p.reconcileRemoteTurn(ctx, codexRuntime,
			subscription.Events(), task, threadID, commands,
			p.discordCommandHandler(task, runtime, skills, report), codexReport)
		if recoverErr != nil {
			return workerprotocol.CompleteRequest{}, recoverErr
		}
		if recovered {
			return workerprotocol.CompleteRequest{Result: result}, nil
		}
	}
	input := remoteDiscordTurnInput(snapshot, runtime, attachments, skills)
	turnID, err := codexRuntime.StartTurn(ctx, threadID, input)
	if err != nil {
		return workerprotocol.CompleteRequest{}, err
	}
	if err := p.client.RecordSubmission(ctx, task, turnID); err != nil {
		return workerprotocol.CompleteRequest{}, err
	}
	result, err := p.waitRemoteTurn(ctx, codexRuntime, subscription.Events(), task, threadID,
		turnID, commands, p.discordCommandHandler(task, runtime, skills, report), codexReport)
	if err != nil {
		interruptTurnBestEffort(codexRuntime, threadID, turnID)
		return workerprotocol.CompleteRequest{}, err
	}
	report("discord.progress", remoteEventPayload(map[string]string{
		"state": "completed", "detail": "本轮处理完成。",
	}))
	return workerprotocol.CompleteRequest{Result: result}, nil
}

func remoteDiscordEventReporter(report func(string, json.RawMessage)) func(string, json.RawMessage) {
	return func(eventType string, payload json.RawMessage) {
		report(eventType, payload)
		if eventType == "turn/started" {
			report("discord.progress", remoteEventPayload(map[string]string{
				"state": "running", "detail": "Codex 正在处理当前消息。",
			}))
		}
	}
}

func remoteDevelopmentSpec(value workerprotocol.DevelopmentSpec) devcontainer.RemoteSpec {
	return devcontainer.RemoteSpec{
		EnvironmentID: value.EnvironmentID, ForumID: value.ForumID,
		ConversationID: value.ConversationID, WorkspaceStatus: value.WorkspaceStatus,
		WorkspaceRelative: value.WorkspaceRelative, WorkspaceBranch: value.WorkspaceBranch,
		Repository: value.Repository, CloneURL: value.CloneURL, DefaultRef: value.DefaultRef,
		BuildRepositoryID: value.BuildRepositoryID, BuildRepository: value.BuildRepository,
		BuildCloneURL: value.BuildCloneURL, BuildDefaultRef: value.BuildDefaultRef,
		EnvironmentStatus: value.EnvironmentStatus, ImageRef: value.ImageRef,
		ImageID: value.ImageID, ContainerName: value.ContainerName,
		ContainerID: value.ContainerID, DataVolume: value.DataVolume,
		HomeVolume: value.HomeVolume, Network: value.Network, RuntimeUser: value.RuntimeUser,
		RuntimeUID: value.RuntimeUID, RuntimeGID: value.RuntimeGID,
		RuntimeHome: value.RuntimeHome, BuildSourceSHA: value.BuildSourceSHA,
	}
}

func protocolDevelopmentState(value devcontainer.RemoteState) workerprotocol.DevelopmentState {
	return workerprotocol.DevelopmentState{DevelopmentSpec: workerprotocol.DevelopmentSpec{
		EnvironmentID: value.EnvironmentID, ForumID: value.ForumID,
		ConversationID: value.ConversationID, WorkspaceStatus: value.WorkspaceStatus,
		WorkspaceRelative: value.WorkspaceRelative, WorkspaceBranch: value.WorkspaceBranch,
		Repository: value.Repository, CloneURL: value.CloneURL, DefaultRef: value.DefaultRef,
		BuildRepositoryID: value.BuildRepositoryID, BuildRepository: value.BuildRepository,
		BuildCloneURL: value.BuildCloneURL, BuildDefaultRef: value.BuildDefaultRef,
		EnvironmentStatus: value.EnvironmentStatus, ImageRef: value.ImageRef,
		ImageID: value.ImageID, ContainerName: value.ContainerName,
		ContainerID: value.ContainerID, DataVolume: value.DataVolume,
		HomeVolume: value.HomeVolume, Network: value.Network, RuntimeUser: value.RuntimeUser,
		RuntimeUID: value.RuntimeUID, RuntimeGID: value.RuntimeGID,
		RuntimeHome: value.RuntimeHome, BuildSourceSHA: value.BuildSourceSHA,
	}, WorkspaceHeadSHA: value.WorkspaceHeadSHA, WorkspaceDirty: value.WorkspaceDirty,
		Error: value.Error}
}

func (p *RemoteProcessor) reportDevelopmentState(ctx context.Context, task *workerprotocol.Task,
	state devcontainer.RemoteState,
) {
	requestCtx, cancel := context.WithTimeout(ctx, p.cfg.ControlTimeout)
	defer cancel()
	if err := p.client.DevelopmentState(requestCtx, task, protocolDevelopmentState(state)); err != nil {
		p.logger.Warn("回传 Discord 开发环境状态失败", zap.Error(err))
	}
}
