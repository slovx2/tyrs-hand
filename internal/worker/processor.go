package worker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/githubtools"
	"github.com/slovx2/tyrs-hand/internal/ports"
	"github.com/slovx2/tyrs-hand/internal/queue"
	platformsettings "github.com/slovx2/tyrs-hand/internal/settings"
	"go.uber.org/zap"
)

type Processor struct {
	cfg       config.Config
	db        *sql.DB
	workspace ports.WorkspaceManager
	control   *ControlClient
	catalog   *githubtools.Catalog
	settings  *platformsettings.Service
	pool      *codex.Pool
	logger    *zap.Logger
}

type jobContext struct {
	Owner           string
	Repository      string
	CloneURL        string
	DefaultBranch   string
	Kind            string
	Number          int
	HeadSHA         string
	ContextVersion  int64
	ProfileName     string
	Model           string
	ReasoningEffort string
	ServiceTier     string
	Sandbox         string
	ApprovalPolicy  string
	NetworkEnabled  bool
}

func NewProcessor(cfg config.Config, db *sql.DB, workspace ports.WorkspaceManager, control *ControlClient, catalog *githubtools.Catalog, settingsService *platformsettings.Service, pool *codex.Pool, logger *zap.Logger) *Processor {
	return &Processor{cfg: cfg, db: db, workspace: workspace, control: control, catalog: catalog, settings: settingsService, pool: pool, logger: logger}
}

func (p *Processor) Process(ctx context.Context, claimed *queue.ClaimedJob) error {
	jobCtx, err := p.loadContext(ctx, claimed.Job)
	if err != nil {
		return err
	}
	credential, err := p.control.GitCredential(ctx, claimed.Capability, "fetch")
	if err != nil {
		return err
	}
	baseRef := "refs/remotes/origin/" + jobCtx.DefaultBranch
	if jobCtx.HeadSHA != "" {
		baseRef = jobCtx.HeadSHA
	}
	branch := fmt.Sprintf("tyrs-hand/%s-%d-%s", jobCtx.Kind, jobCtx.Number, shortID(claimed.WorkItemID))
	workspace, err := p.workspace.Ensure(ctx, ports.WorkspaceSpec{
		RepositoryID: claimed.RepositoryID.String(), WorkItemID: claimed.WorkItemID.String(),
		CloneURL: jobCtx.CloneURL, BaseRef: baseRef, Branch: branch,
	}, credential)
	if err != nil {
		return err
	}
	if err := p.recordWorkspace(ctx, claimed, workspace, baseRef); err != nil {
		return err
	}
	defer p.refreshWorkspaceState(context.Background(), claimed.WorkItemID, workspace.WorktreePath)
	skills, err := resolveSkills(workspace.WorktreePath, claimed.Skills)
	if err != nil {
		return err
	}
	githubSpec, err := p.catalog.DynamicToolSpecFor(append(append([]string{}, claimed.AllowedTools...), claimed.DangerousActions...))
	if err != nil {
		return err
	}
	gitSpec := localGitSpec()
	provider, err := p.settings.AgentProvider(ctx)
	if err != nil {
		return err
	}
	signature := provider.ConfigSignature
	if signature == "" {
		signature = "default"
	}
	codexHome := filepath.Join(p.cfg.CodexHomeRoot, "pools", claimed.RepositoryID.String(), claimed.AgentProfileID.String(), signature[:min(16, len(signature))])
	provider, environment, err := p.settings.PrepareCodexHome(ctx, codexHome, filepath.Join(p.cfg.CodexHomeRoot, "shared"))
	if err != nil {
		return err
	}
	poolKey := claimed.RepositoryID.String() + "/" + claimed.AgentProfileID.String() + "/" + signature
	client, err := p.pool.Acquire(ctx, poolKey, workspace.WorktreePath, codexHome, environment)
	if err != nil {
		return err
	}
	runtime := codex.NewRuntime(client)
	if jobCtx.Model == "" {
		jobCtx.Model = provider.Model
	}
	if jobCtx.ReasoningEffort == "" {
		jobCtx.ReasoningEffort = provider.Reasoning
	}
	if jobCtx.ServiceTier == "" {
		jobCtx.ServiceTier = provider.ServiceTier
	}
	options := ports.ThreadOptions{
		CWD: workspace.WorktreePath, Model: jobCtx.Model, ReasoningEffort: jobCtx.ReasoningEffort,
		ServiceTier: jobCtx.ServiceTier, Sandbox: jobCtx.Sandbox, ApprovalPolicy: jobCtx.ApprovalPolicy,
		NetworkEnabled: jobCtx.NetworkEnabled, DynamicTools: []ports.DynamicToolSpec{githubSpec, gitSpec},
		DeveloperInstructions: "Follow repository AGENTS.md and the explicitly attached skills. Use only the authorized GitHub work item and current worktree.",
	}
	if err := runtime.ValidateSkills(ctx, workspace.WorktreePath, skills); err != nil {
		return err
	}
	threadDBID, threadID, err := p.ensureThread(ctx, runtime, claimed.Job, options, codexHome, signature)
	if err != nil {
		return err
	}
	unbind, err := p.pool.Bind(poolKey, threadID, func(toolCtx context.Context, request codex.ToolCallRequest) (codex.ToolCallResult, error) {
		return p.handleTool(toolCtx, claimed, workspace, branch, request)
	})
	if err != nil {
		return err
	}
	defer unbind()
	turnID, err := runtime.StartTurn(ctx, threadID, ports.TurnInput{
		Text: claimed.Instruction, ClientUserMessageID: claimed.ID.String(), Skills: skills,
	})
	if err != nil {
		return err
	}
	if err := p.waitTurn(ctx, runtime, client.Events(), claimed, threadDBID, threadID, turnID); err != nil {
		_ = runtime.InterruptTurn(context.Background(), threadID, turnID)
		return err
	}
	if err := p.persistMemorySummary(ctx, claimed.WorkItemID, threadDBID, threadID); err != nil {
		p.logger.Warn("持久化 Work Item Summary 失败", zap.Error(err), zap.String("work_item_id", claimed.WorkItemID.String()))
	}
	_, err = p.db.ExecContext(ctx, `UPDATE agent_threads SET last_turn_id = $2, last_used_at = now(), expires_at = now() + interval '30 days' WHERE id = $1`, threadDBID, turnID)
	return err
}

func (p *Processor) handleTool(ctx context.Context, claimed *queue.ClaimedJob, workspace ports.Workspace, branch string, request codex.ToolCallRequest) (codex.ToolCallResult, error) {
	namespace := ""
	if request.Namespace != nil {
		namespace = *request.Namespace
	}
	if namespace == "github" {
		return p.control.CallTool(ctx, claimed.Capability, request)
	}
	if namespace != "git" {
		return codex.ToolCallResult{}, errors.New("未知 dynamic tool namespace")
	}
	switch request.Tool {
	case "status":
		status, err := p.workspace.Status(ctx, workspace.WorktreePath)
		return codex.TextToolResult(status, err == nil), err
	case "publish_branch":
		credential, err := p.control.GitCredential(ctx, claimed.Capability, "push")
		if err != nil {
			return codex.ToolCallResult{}, err
		}
		sha, err := p.workspace.Publish(ctx, workspace.WorktreePath, branch, credential)
		return codex.TextToolResult(fmt.Sprintf(`{"branch":%q,"sha":%q}`, branch, sha), err == nil), err
	default:
		return codex.ToolCallResult{}, fmt.Errorf("本地 Git 工具 %s 未授权", request.Tool)
	}
}
