package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/domain"
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
	if claimed.SourceType == domain.JobSourceDiscordConversation {
		return p.processDiscordConversation(ctx, claimed)
	}
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
		DeveloperInstructions: "Follow repository AGENTS.md and the explicitly attached skills. Use only the authorized GitHub work item and current worktree. Use git.commit for commits and git.publish_branch for pushes; do not write shared Git metadata with shell commands.",
	}
	if err := runtime.ValidateSkills(ctx, workspace.WorktreePath, skills); err != nil {
		return err
	}
	threadSignature := threadConfigSignature(signature, options)
	threadDBID, threadID, err := p.ensureThread(ctx, runtime, claimed.Job, options, codexHome, threadSignature)
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
		OutputSchema: agentOutcomeSchema(),
	})
	if err != nil {
		return err
	}
	if err := p.waitTurn(ctx, runtime, client.Events(), claimed, threadDBID, threadID, turnID); err != nil {
		_ = runtime.InterruptTurn(context.Background(), threadID, turnID)
		return err
	}
	outcome, err := p.loadAgentOutcome(ctx, threadDBID, turnID)
	if err != nil {
		return err
	}
	if err := p.persistMemorySummary(ctx, claimed.WorkItemID, threadID, outcome.Summary); err != nil {
		p.logger.Warn("持久化 Work Item Summary 失败", zap.Error(err), zap.String("work_item_id", claimed.WorkItemID.String()))
	}
	if _, err = p.db.ExecContext(ctx, `UPDATE agent_threads SET last_turn_id = $2, last_used_at = now(), expires_at = now() + interval '30 days' WHERE id = $1`, threadDBID, turnID); err != nil {
		return err
	}
	if outcome.Status == domain.JobBlocked {
		return &blockedError{summary: outcome.Summary}
	}
	return nil
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
	return p.auditLocalToolCall(ctx, claimed, request, func() (codex.ToolCallResult, error) {
		return p.executeLocalTool(ctx, claimed, workspace, branch, request)
	})
}

func (p *Processor) executeLocalTool(ctx context.Context, claimed *queue.ClaimedJob, workspace ports.Workspace, branch string, request codex.ToolCallRequest) (codex.ToolCallResult, error) {
	switch request.Tool {
	case "status":
		status, err := p.workspace.Status(ctx, workspace.WorktreePath)
		return codex.TextToolResult(status, err == nil), err
	case "commit":
		var arguments struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(request.Arguments, &arguments); err != nil {
			return codex.ToolCallResult{}, fmt.Errorf("解析 Git 提交参数: %w", err)
		}
		sha, err := p.workspace.Commit(ctx, workspace.WorktreePath, arguments.Message)
		if err == nil {
			err = p.recordCommittedHead(ctx, claimed.WorkItemID, sha)
		}
		return codex.TextToolResult(fmt.Sprintf(`{"sha":%q}`, sha), err == nil), err
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

func (p *Processor) auditLocalToolCall(ctx context.Context, claimed *queue.ClaimedJob, request codex.ToolCallRequest, execute func() (codex.ToolCallResult, error)) (codex.ToolCallResult, error) {
	if request.ThreadID == "" || request.TurnID == "" || request.CallID == "" {
		return codex.ToolCallResult{}, errors.New("本地 Tool Call 缺少 thread、turn 或 call ID")
	}
	var id uuid.UUID
	err := p.db.QueryRowContext(ctx, `
		INSERT INTO tool_calls(job_attempt_id, thread_id, turn_id, call_id, namespace, tool, arguments)
		VALUES ($1, $2, $3, $4, 'git', $5, $6)
		ON CONFLICT(thread_id, turn_id, call_id) DO NOTHING
		RETURNING id`, claimed.AttemptID, request.ThreadID, request.TurnID, request.CallID, request.Tool, request.Arguments).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return p.previousLocalToolResult(ctx, request)
	}
	if err != nil {
		return codex.ToolCallResult{}, err
	}
	result, err := execute()
	if err != nil {
		_, _ = p.db.ExecContext(ctx, `UPDATE tool_calls SET status = 'failed', error = $2, finished_at = now() WHERE id = $1`, id, err.Error())
		return result, err
	}
	resultJSON, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		return codex.ToolCallResult{}, marshalErr
	}
	if _, err := p.db.ExecContext(ctx, `UPDATE tool_calls SET status = 'completed', result = $2, finished_at = now() WHERE id = $1`, id, resultJSON); err != nil {
		return codex.ToolCallResult{}, err
	}
	return result, nil
}

func (p *Processor) previousLocalToolResult(ctx context.Context, request codex.ToolCallRequest) (codex.ToolCallResult, error) {
	var status string
	var resultJSON []byte
	var message sql.NullString
	err := p.db.QueryRowContext(ctx, `
		SELECT status, result, error FROM tool_calls
		WHERE thread_id = $1 AND turn_id = $2 AND call_id = $3`,
		request.ThreadID, request.TurnID, request.CallID).Scan(&status, &resultJSON, &message)
	if err != nil {
		return codex.ToolCallResult{}, err
	}
	if status == "completed" {
		var result codex.ToolCallResult
		if err := json.Unmarshal(resultJSON, &result); err != nil {
			return codex.ToolCallResult{}, err
		}
		return result, nil
	}
	if status == "failed" {
		return codex.ToolCallResult{}, errors.New(message.String)
	}
	return codex.ToolCallResult{}, errors.New("同一本地 Tool Call 正在执行，不能重复提交")
}

func (p *Processor) recordCommittedHead(ctx context.Context, workItemID uuid.UUID, sha string) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE worktrees SET head_sha = $2, dirty = false, last_used_at = now() WHERE work_item_id = $1`, workItemID, sha); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE work_items SET head_sha = $2, updated_at = now() WHERE id = $1`, workItemID, sha); err != nil {
		return err
	}
	return tx.Commit()
}
