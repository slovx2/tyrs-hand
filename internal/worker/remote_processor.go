package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/codexsettings"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/devcontainer"
	"github.com/slovx2/tyrs-hand/internal/githubtools"
	"github.com/slovx2/tyrs-hand/internal/ports"
	"github.com/slovx2/tyrs-hand/internal/replygate"
	"github.com/slovx2/tyrs-hand/internal/settings"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"go.uber.org/zap"
)

type RemoteProcessor struct {
	cfg         config.Config
	client      *workerprotocol.Client
	workspace   ports.WorkspaceManager
	catalog     *githubtools.Catalog
	pool        *codex.Pool
	development *devcontainer.Manager
	logger      *zap.Logger
}

func NewRemoteProcessor(cfg config.Config, client *workerprotocol.Client,
	workspace ports.WorkspaceManager, catalog *githubtools.Catalog, pool *codex.Pool,
	development *devcontainer.Manager, logger *zap.Logger,
) *RemoteProcessor {
	return &RemoteProcessor{cfg: cfg, client: client, workspace: workspace, catalog: catalog,
		pool: pool, development: development, logger: logger}
}

func (p *RemoteProcessor) ProcessRemote(ctx context.Context, task *workerprotocol.Task,
	commands <-chan workerprotocol.RunCommand,
	report func(string, json.RawMessage),
) (workerprotocol.CompleteRequest, error) {
	if task.Snapshot.Runtime.ProviderType != "api-key" {
		return workerprotocol.CompleteRequest{}, errors.New("分布式 Worker 只支持 API Key Provider")
	}
	if task.Claimed.SourceType == codexcontrol.SourceDiscord {
		return p.processRemoteDiscord(ctx, task, commands, report)
	}
	result, err := p.processRemoteGitHub(ctx, task, commands, report)
	return workerprotocol.CompleteRequest{Result: result}, err
}

func (p *RemoteProcessor) ProcessDevelopmentOperation(ctx context.Context,
	operation *workerprotocol.DevelopmentOperation,
) error {
	return p.development.RunRemoteOperation(ctx, devcontainer.RemoteOperation{
		Operation: operation.Operation, ContainerName: operation.ContainerName,
		ImageRef: operation.ImageRef, DataVolume: operation.DataVolume,
		HomeVolume: operation.HomeVolume, Network: operation.Network,
		Workspace: operation.Workspace, ConversationIDs: operation.ConversationIDs,
	})
}

func (p *RemoteProcessor) processRemoteGitHub(ctx context.Context, task *workerprotocol.Task,
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
	defer cleanupBrowserTask(p.cfg, claimed.ID.String())
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
	credential, err := p.client.RuntimeCredential(ctx, task)
	if err != nil {
		return codexcontrol.TurnResult{}, err
	}
	signature := task.Snapshot.Runtime.ConfigSignature
	if signature == "" {
		signature = "default"
	}
	codexHome := filepath.Join(p.cfg.CodexHomeRoot, "pools", claimed.RepositoryID.String(),
		claimed.AgentProfileID.String(), signature[:min(16, len(signature))])
	environment, err := prepareRemoteCodexHome(codexHome, credential,
		task.Snapshot.Runtime.GlobalAgents)
	if err != nil {
		return codexcontrol.TurnResult{}, err
	}
	if err := replygate.Install(codexHome); err != nil {
		return codexcontrol.TurnResult{}, fmt.Errorf("安装 GitHub 回复 Stop Hook: %w", err)
	}
	poolKey := "job/" + claimed.ID.String()
	client, err := p.pool.Acquire(ctx, poolKey, workspace.WorktreePath, codexHome, environment)
	if err != nil {
		return codexcontrol.TurnResult{}, err
	}
	defer func() {
		if closeErr := p.pool.Release(poolKey); closeErr != nil {
			p.logger.Warn("关闭远程 Job Codex App Server 失败", zap.Error(closeErr))
		}
	}()
	runtime := codex.NewRuntime(client)
	settings := task.Snapshot.Runtime
	options := ports.ThreadOptions{
		CWD: workspace.WorktreePath, Model: settings.Model,
		ReasoningEffort: settings.ReasoningEffort,
		ServiceTier:     codexsettings.RuntimeServiceTier(settings.ServiceTier),
		Sandbox:         settings.Sandbox, ApprovalPolicy: settings.ApprovalPolicy,
		NetworkEnabled:        settings.NetworkEnabled,
		DynamicTools:          withBrowserTools(p.cfg, githubSpec, localGitSpec(), githubReplySpec()),
		RuntimeConfig:         codexRuntimeConfig(environment, p.cfg.WorkerDataRoot, p.cfg),
		DeveloperInstructions: browserDeveloperInstructions(p.cfg, "Follow repository AGENTS.md and the explicitly attached skills. Use only the authorized GitHub work item and current worktree. Use git.commit for commits and git.publish_branch for pushes. After all business actions, call tyrs_hand.reply_to_github exactly once with the user-facing result, then provide a natural final answer."),
	}
	if err := runtime.ValidateSkills(ctx, workspace.WorktreePath, skills); err != nil {
		return codexcontrol.TurnResult{}, err
	}
	threadSignature := threadConfigSignature(signature, options)
	threadID, err := p.ensureRemoteThread(ctx, runtime, task, options, codexHome, threadSignature)
	if err != nil {
		return codexcontrol.TurnResult{}, err
	}
	if err := replygate.Initialize(codexHome, threadID, claimed.ID.String(), true,
		p.cfg.GitHubReplyGateMaxBlocks); err != nil {
		return codexcontrol.TurnResult{}, err
	}
	unbind, err := p.pool.Bind(poolKey, threadID, func(toolCtx context.Context,
		request codex.ToolCallRequest,
	) (codex.ToolCallResult, error) {
		return p.handleRemoteGitHubTool(toolCtx, task, codexHome, workspace, branch, request,
			report)
	})
	if err != nil {
		return codexcontrol.TurnResult{}, err
	}
	defer unbind()
	if claimed.Recovering {
		if result, recovered, recoverErr := p.reconcileRemoteTurn(ctx, runtime, task,
			threadID, commands, nil, report); recoverErr != nil {
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
	if err := p.client.RecordSubmission(ctx, task, turnID); err != nil {
		return codexcontrol.TurnResult{}, err
	}
	result, err := p.waitRemoteTurn(ctx, runtime, client.Events(), task, threadID, turnID,
		commands, nil, report)
	if err != nil {
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

func prepareRemoteCodexHome(home string, credential workerprotocol.RuntimeCredential,
	globalAgents string,
) ([]string, error) {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, err
	}
	if err := settings.WriteGlobalAgents(filepath.Join(home, "AGENTS.md"), globalAgents); err != nil {
		return nil, err
	}
	auth, _ := json.Marshal(map[string]string{"auth_mode": "apikey", "OPENAI_API_KEY": credential.APIKey})
	temporary := filepath.Join(home, "auth.json.tmp")
	if err := os.WriteFile(temporary, auth, 0o600); err != nil {
		return nil, err
	}
	if err := os.Chmod(temporary, 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(temporary, filepath.Join(home, "auth.json")); err != nil {
		return nil, err
	}
	if credential.BaseURL != "" {
		if err := settings.WriteProviderConfig(filepath.Join(home, "config.toml"), credential.BaseURL); err != nil {
			return nil, err
		}
	}
	environment := []string(nil)
	if credential.BaseURL != "" {
		environment = append(environment, "OPENAI_BASE_URL="+credential.BaseURL)
	}
	if credential.ProxyURL != "" {
		environment = append(environment, "HTTP_PROXY="+credential.ProxyURL,
			"HTTPS_PROXY="+credential.ProxyURL)
	}
	return environment, nil
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
