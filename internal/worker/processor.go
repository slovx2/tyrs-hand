package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/codexsettings"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/githubtools"
	"github.com/slovx2/tyrs-hand/internal/hostworker"
	"github.com/slovx2/tyrs-hand/internal/ports"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"go.uber.org/zap"
)

type Processor struct {
	cfg         config.Config
	client      *workerprotocol.Client
	workspace   ports.WorkspaceManager
	catalog     *githubtools.Catalog
	workspaces  *workspaceCodexRegistry
	journals    *journalStore
	logger      *zap.Logger
	hostRuntime *hostworker.Runtime
}

func (p *Processor) UseHostRuntime(runtime *hostworker.Runtime) {
	p.hostRuntime = runtime
}

func (p *Processor) HeartbeatMetadata() map[string]any {
	if p.hostRuntime != nil {
		return map[string]any{"host": map[string]any{
			"home": p.hostRuntime.Home(), "codexHome": p.hostRuntime.CodexHome(),
			"workspaceRoot": p.hostRuntime.WorkspaceRoot(), "appServer": "running",
		}}
	}
	if p.workspaces == nil {
		return nil
	}
	return map[string]any{"modelCatalogs": p.workspaces.modelCatalogs()}
}

func NewProcessor(ctx context.Context, cfg config.Config, client *workerprotocol.Client,
	workspace ports.WorkspaceManager, catalog *githubtools.Catalog, logger *zap.Logger,
) *Processor {
	processor := &Processor{cfg: cfg, client: client, workspace: workspace, catalog: catalog,
		logger: logger}
	if journals, err := newJournalStore(cfg.WorkerDataRoot); err == nil {
		processor.journals = journals
	} else {
		logger.Error("初始化 Desktop Run Journal 失败", zap.Error(err))
	}
	processor.workspaces = newWorkspaceCodexRegistry(ctx, processor)
	return processor
}

func (p *Processor) Process(ctx context.Context, task *workerprotocol.Task,
	commands <-chan workerprotocol.RunCommand,
	report func(string, json.RawMessage),
) (workerprotocol.CompleteRequest, error) {
	if task.Claimed.SourceType == codexcontrol.SourceWorkspace {
		return p.processRemoteDiscord(ctx, task, commands, report)
	}
	result, err := p.processRemoteGitHub(ctx, task, commands, report)
	return workerprotocol.CompleteRequest{Result: result}, err
}

func (p *Processor) processRemoteGitHub(ctx context.Context, task *workerprotocol.Task,
	commands <-chan workerprotocol.RunCommand,
	report func(string, json.RawMessage),
) (codexcontrol.TurnResult, error) {
	job := task.Snapshot.GitHub
	if job == nil {
		return codexcontrol.TurnResult{}, errors.New("github 任务缺少快照")
	}
	claimed := &task.Claimed
	fetchCredential, err := p.client.GitCredential(ctx, task, "fetch", "", "")
	if err != nil {
		return codexcontrol.TurnResult{}, err
	}
	defer cleanupBrowserTask(p.cfg, claimed.ID.String(), "worker")
	baseRef := "refs/remotes/origin/" + job.DefaultBranch
	if job.Kind == "pull_request" {
		baseRef = fmt.Sprintf("refs/remotes/pull/%d", job.Number)
	} else if job.HeadSHA != "" {
		baseRef = job.HeadSHA
	}
	branch := fmt.Sprintf("tyrs-hand/%s-%d-%s", job.Kind, job.Number,
		shortID(claimed.WorkItemID))
	workspace, err := p.workspace.Ensure(ctx, ports.WorkspaceSpec{
		RepositoryID: claimed.RepositoryID.String(), WorkItemID: claimed.WorkItemID.String(),
		CloneURL: job.CloneURL, BaseRef: baseRef, Branch: branch,
	}, fetchCredential)
	if err != nil {
		return codexcontrol.TurnResult{}, err
	}
	workspaceState := workerprotocol.WorkspaceState{CachePath: workspace.CachePath,
		WorktreePath: workspace.WorktreePath, Branch: workspace.Branch, BaseSHA: baseRef,
		HeadSHA: workspace.HeadSHA, Status: "ready"}
	if err := p.client.WorkspaceState(ctx, task, workspaceState); err != nil {
		return codexcontrol.TurnResult{}, err
	}
	defer func() {
		stateCtx, cancel := context.WithTimeout(context.Background(), p.cfg.ControlTimeout)
		defer cancel()
		status, statusErr := p.workspace.Status(stateCtx, workspace.WorktreePath)
		workspaceState.Dirty = remoteWorkspaceDirty(status)
		if statusErr != nil {
			workspaceState.Status, workspaceState.Error = "failed", statusErr.Error()
		}
		_ = p.client.WorkspaceState(stateCtx, task, workspaceState)
	}()
	skills, err := resolveSkills(workspace.WorktreePath, claimed.Skills)
	if err != nil {
		return codexcontrol.TurnResult{}, err
	}
	githubSpec, err := p.catalog.DynamicToolSpecFor(withoutGenericReply(append(
		append([]string{}, claimed.AllowedTools...), claimed.DangerousActions...)))
	if err != nil {
		return codexcontrol.TurnResult{}, err
	}
	if p.hostRuntime == nil {
		return codexcontrol.TurnResult{}, errors.New("宿主 Codex Runtime 尚未启动")
	}
	codexHome := p.hostRuntime.CodexHome()
	runtimeConfig := prepareCodexRuntime(p.cfg.WorkerDataRoot, p.cfg, claimed.ID.String())
	client := p.hostRuntime.Client()
	runtime := codex.NewRuntime(client)
	settings := task.Snapshot.Runtime
	instructions := ""
	if task.Snapshot.GitHubAgent != nil {
		instructions = task.Snapshot.GitHubAgent.Instructions
	}
	options := workerThreadOptions(ports.ThreadOptions{
		CWD: workspace.WorktreePath, Model: settings.Model,
		ReasoningEffort: settings.ReasoningEffort,
		ServiceTier:     codexsettings.RuntimeServiceTier(settings.ServiceTier),
		NetworkEnabled:  settings.NetworkEnabled,
		DynamicTools:    withBrowserTools(p.cfg, githubSpec, localGitSpec(true), githubReplySpec()),
		RuntimeConfig:   runtimeConfig,
		DeveloperInstructions: browserDeveloperInstructions(p.cfg,
			instructions+"\n\nFollow repository AGENTS.md and the explicitly attached skills. Use only the authorized GitHub work item and current worktree. Use git.commit for commits and git.publish_branch for pushes. After all business actions, call tyrs_hand.reply_to_github exactly once with the user-facing result, then provide a natural final answer."),
	})
	if err := runtime.ValidateSkills(ctx, workspace.WorktreePath, skills); err != nil {
		return codexcontrol.TurnResult{}, err
	}
	threadID, err := p.ensureRemoteThread(ctx, runtime, task, options, report)
	if err != nil {
		return codexcontrol.TurnResult{}, err
	}
	unbind := p.hostRuntime.BindTool(threadID, func(toolCtx context.Context,
		request codex.ToolCallRequest,
	) (codex.ToolCallResult, error) {
		return p.handleRemoteGitHubTool(toolCtx, task, codexHome, workspace, branch, request,
			report)
	})
	defer unbind()
	subscription := client.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	defer subscription.Close()
	if claimed.Recovering {
		if result, recovered, recoverErr := p.reconcileRemoteTurn(ctx, runtime, subscription.Events(),
			task, threadID, commands, nil, report); recoverErr != nil {
			return codexcontrol.TurnResult{}, recoverErr
		} else if recovered {
			return result, nil
		}
	}
	turnID, err := runtime.StartTurn(ctx, threadID, ports.TurnInput{
		Text: claimed.Instruction, ClientUserMessageID: claimed.ID.String(), Skills: skills,
		AdditionalContext: remoteGitHubAdditionalContext(job, workspace),
	})
	if err != nil {
		return codexcontrol.TurnResult{}, err
	}
	reportRuntimeSettingsApplied(report, task.Snapshot.Runtime, "turn/start")
	if err := p.client.RecordSubmission(ctx, task, turnID); err != nil {
		return codexcontrol.TurnResult{}, err
	}
	result, err := p.waitRemoteTurn(ctx, runtime, subscription.Events(), task, threadID, turnID,
		commands, nil, report)
	if needsCleanupInterrupt(err) {
		interruptTurnBestEffort(runtime, threadID, turnID)
	}
	return result, err
}

func remoteWorkspaceDirty(status string) bool {
	for _, line := range strings.Split(strings.TrimSpace(status), "\n") {
		if line != "" && !strings.HasPrefix(line, "##") {
			return true
		}
	}
	return false
}

func remoteGitHubAdditionalContext(job *workerprotocol.GitHubSnapshot,
	workspace ports.Workspace,
) map[string]ports.AdditionalContextEntry {
	contextJob := jobContext{Owner: job.Owner, Repository: job.Repository, Kind: job.Kind,
		Number: job.Number, HTMLURL: job.HTMLURL, HeadRepository: job.HeadRepository,
		HeadRef: job.HeadRef, HeadSHA: job.HeadSHA, BaseRef: job.BaseRef, BaseSHA: job.BaseSHA}
	return githubWorkItemAdditionalContext(contextJob, workspace)
}

func remoteEventPayload(value any) json.RawMessage {
	data, _ := json.Marshal(value)
	return data
}

func trimError(value error) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(value.Error())
}
