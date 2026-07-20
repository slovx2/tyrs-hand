package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/devcontainer"
	"github.com/slovx2/tyrs-hand/internal/githubtools"
	"github.com/slovx2/tyrs-hand/internal/ports"
	"github.com/slovx2/tyrs-hand/internal/replygate"
	platformsettings "github.com/slovx2/tyrs-hand/internal/settings"
	"go.uber.org/zap"
)

type Processor struct {
	cfg         config.Config
	db          *sql.DB
	redis       *redis.Client
	workspace   ports.WorkspaceManager
	control     *ControlClient
	controls    *codexcontrol.Repository
	catalog     *githubtools.Catalog
	settings    *platformsettings.Service
	pool        *codex.Pool
	development *devcontainer.Manager
	logger      *zap.Logger
}

type jobContext struct {
	Owner           string
	Repository      string
	CloneURL        string
	DefaultBranch   string
	Kind            string
	Number          int
	HeadSHA         string
	HeadRef         string
	HeadRepository  string
	BaseSHA         string
	BaseRef         string
	HTMLURL         string
	ContextVersion  int64
	ProfileName     string
	Model           string
	ReasoningEffort string
	ServiceTier     string
	Sandbox         string
	ApprovalPolicy  string
	NetworkEnabled  bool
}

func NewProcessor(cfg config.Config, db *sql.DB, redisClient *redis.Client, workspace ports.WorkspaceManager,
	control *ControlClient, controls *codexcontrol.Repository, catalog *githubtools.Catalog,
	settingsService *platformsettings.Service, pool *codex.Pool, development *devcontainer.Manager,
	logger *zap.Logger,
) *Processor {
	return &Processor{cfg: cfg, db: db, redis: redisClient, workspace: workspace, control: control,
		controls: controls, catalog: catalog, settings: settingsService, pool: pool,
		development: development, logger: logger}
}

func (p *Processor) Process(ctx context.Context, claimed *codexcontrol.ClaimedControl) (codexcontrol.TurnResult, error) {
	if claimed.Operation == "interrupt" {
		return codexcontrol.TurnResult{Evidence: "interrupt_when_idle"}, nil
	}
	if claimed.SourceType == codexcontrol.SourceDiscord {
		return p.processDiscordConversation(ctx, claimed)
	}
	jobCtx, err := p.loadContext(ctx, claimed.Intent)
	if err != nil {
		return codexcontrol.TurnResult{}, err
	}
	credential, err := p.control.GitCredential(ctx, claimed.Capability, "fetch")
	if err != nil {
		return codexcontrol.TurnResult{}, err
	}
	baseRef := "refs/remotes/origin/" + jobCtx.DefaultBranch
	if jobCtx.Kind == "pull_request" {
		baseRef = fmt.Sprintf("refs/remotes/pull/%d", jobCtx.Number)
	} else if jobCtx.HeadSHA != "" {
		baseRef = jobCtx.HeadSHA
	}
	branch := fmt.Sprintf("tyrs-hand/%s-%d-%s", jobCtx.Kind, jobCtx.Number, shortID(claimed.WorkItemID))
	workspace, err := p.workspace.Ensure(ctx, ports.WorkspaceSpec{
		RepositoryID: claimed.RepositoryID.String(), WorkItemID: claimed.WorkItemID.String(),
		CloneURL: jobCtx.CloneURL, BaseRef: baseRef, Branch: branch,
	}, credential)
	if err != nil {
		return codexcontrol.TurnResult{}, err
	}
	if err := p.recordWorkspace(ctx, claimed, workspace, baseRef); err != nil {
		return codexcontrol.TurnResult{}, err
	}
	defer p.refreshWorkspaceState(context.Background(), claimed.WorkItemID, workspace.WorktreePath)
	skills, err := resolveSkills(workspace.WorktreePath, claimed.Skills)
	if err != nil {
		return codexcontrol.TurnResult{}, err
	}
	githubSpec, err := p.catalog.DynamicToolSpecFor(withoutGenericReply(append(append([]string{}, claimed.AllowedTools...), claimed.DangerousActions...)))
	if err != nil {
		return codexcontrol.TurnResult{}, err
	}
	gitSpec := localGitSpec()
	provider, err := p.settings.AgentProvider(ctx)
	if err != nil {
		return codexcontrol.TurnResult{}, err
	}
	signature := provider.ConfigSignature
	if signature == "" {
		signature = "default"
	}
	codexHome := filepath.Join(p.cfg.CodexHomeRoot, "pools", claimed.RepositoryID.String(), claimed.AgentProfileID.String(), signature[:min(16, len(signature))])
	provider, environment, err := p.settings.PrepareCodexHome(ctx, codexHome, filepath.Join(p.cfg.CodexHomeRoot, "shared"))
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
			p.logger.Warn("关闭 Job Codex App Server 失败", zap.Error(closeErr), zap.String("job_id", claimed.ID.String()))
		}
	}()
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
		NetworkEnabled: jobCtx.NetworkEnabled, DynamicTools: []ports.DynamicToolSpec{githubSpec, gitSpec, githubReplySpec()},
		RuntimeConfig:         codexRuntimeConfig(nil, p.cfg.WorkerDataRoot),
		DeveloperInstructions: "Follow repository AGENTS.md and the explicitly attached skills. Use only the authorized GitHub work item and current worktree. This is a temporary lightweight worktree: the platform does not install dependencies or prepare toolchains, and local builds, tests, and debugging are not recommended unless the user explicitly requests them and the required tools are already available. Use git.commit for commits and git.publish_branch for pushes; do not write shared Git metadata with shell commands. After all business actions, call tyrs_hand.reply_to_github exactly once with the user-facing result, then provide a natural final answer.",
	}
	if err := runtime.ValidateSkills(ctx, workspace.WorktreePath, skills); err != nil {
		return codexcontrol.TurnResult{}, err
	}
	threadSignature := threadConfigSignature(signature, options)
	threadID, err := p.ensureThread(ctx, runtime, claimed, options, codexHome, threadSignature)
	if err != nil {
		return codexcontrol.TurnResult{}, err
	}
	if err := replygate.Initialize(codexHome, threadID, claimed.ID.String(), true,
		p.cfg.GitHubReplyGateMaxBlocks); err != nil {
		return codexcontrol.TurnResult{}, err
	}
	p.syncReplyGate(ctx, claimed, codexHome, threadID)
	unbind, err := p.pool.Bind(poolKey, threadID, func(toolCtx context.Context, request codex.ToolCallRequest) (codex.ToolCallResult, error) {
		return p.handleTool(toolCtx, claimed, codexHome, workspace, branch, request)
	})
	if err != nil {
		return codexcontrol.TurnResult{}, err
	}
	defer unbind()
	if claimed.Recovering {
		if result, recovered, reconcileErr := p.reconcileTurn(ctx, runtime, claimed, threadID); reconcileErr != nil {
			return codexcontrol.TurnResult{}, reconcileErr
		} else if recovered {
			p.syncReplyGate(ctx, claimed, codexHome, threadID)
			return result, nil
		}
	}
	turnID, err := runtime.StartTurn(ctx, threadID, ports.TurnInput{
		Text: claimed.Instruction, ClientUserMessageID: claimed.ID.String(), Skills: skills,
		AdditionalContext: githubWorkItemAdditionalContext(jobCtx, workspace),
	})
	if err != nil {
		return codexcontrol.TurnResult{}, err
	}
	if err := p.controls.RecordSubmission(ctx, claimed, turnID); err != nil {
		return codexcontrol.TurnResult{}, err
	}
	result, err := p.waitTurn(ctx, runtime, client.Events(), claimed, threadID, turnID)
	if err != nil {
		interruptTurnBestEffort(runtime, threadID, turnID)
		return codexcontrol.TurnResult{}, err
	}
	p.syncReplyGate(ctx, claimed, codexHome, threadID)
	return result, nil
}

func (p *Processor) syncReplyGate(ctx context.Context, claimed *codexcontrol.ClaimedControl,
	codexHome, threadID string,
) {
	state, err := replygate.Read(codexHome, threadID)
	if err != nil {
		return
	}
	var delivered bool
	if err := p.db.QueryRowContext(ctx, `SELECT reply_status = 'delivered'
		FROM codex_turn_intents WHERE id = $1`, claimed.ID).Scan(&delivered); err == nil && delivered && !state.Delivered {
		if err := replygate.MarkDelivered(codexHome, threadID); err == nil {
			state.Delivered = true
		}
	}
	_, _ = p.db.ExecContext(ctx, `UPDATE codex_turn_intents SET reply_hook_block_count = $2,
		updated_at = now() WHERE id = $1`, claimed.ID, state.BlockCount)
}

func (p *Processor) handleTool(ctx context.Context, claimed *codexcontrol.ClaimedControl,
	codexHome string, workspace ports.Workspace, branch string, request codex.ToolCallRequest,
) (codex.ToolCallResult, error) {
	namespace := ""
	if request.Namespace != nil {
		namespace = *request.Namespace
	}
	if namespace == "github" || namespace == "tyrs_hand" {
		result, err := p.control.CallTool(ctx, claimed.Capability, request)
		if err == nil && namespace == "tyrs_hand" && request.Tool == "reply_to_github" {
			if gateErr := replygate.MarkDelivered(codexHome, request.ThreadID); gateErr != nil {
				return codex.ToolCallResult{}, gateErr
			}
		}
		return result, err
	}
	if namespace != "git" {
		return codex.ToolCallResult{}, errors.New("未知 dynamic tool namespace")
	}
	return p.auditLocalToolCall(ctx, claimed, request, func() (codex.ToolCallResult, error) {
		return p.executeLocalTool(ctx, claimed, workspace, branch, request)
	})
}

func (p *Processor) executeLocalTool(ctx context.Context, claimed *codexcontrol.ClaimedControl, workspace ports.Workspace, branch string, request codex.ToolCallRequest) (codex.ToolCallResult, error) {
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

func (p *Processor) auditLocalToolCall(ctx context.Context, claimed *codexcontrol.ClaimedControl, request codex.ToolCallRequest, execute func() (codex.ToolCallResult, error)) (codex.ToolCallResult, error) {
	if request.ThreadID == "" || request.TurnID == "" || request.CallID == "" {
		return codex.ToolCallResult{}, errors.New("本地 Tool Call 缺少 thread、turn 或 call ID")
	}
	var id uuid.UUID
	err := p.db.QueryRowContext(ctx, `
		INSERT INTO tool_calls(run_id, intent_id, thread_id, turn_id, call_id, namespace, tool, arguments)
		VALUES ($1, $2, $3, $4, $5, 'git', $6, $7)
		ON CONFLICT(thread_id, turn_id, call_id) DO NOTHING
		RETURNING id`, claimed.RunID, claimed.ID, request.ThreadID, request.TurnID, request.CallID, request.Tool, request.Arguments).Scan(&id)
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
		WHERE thread_id = $1 AND turn_id = $2 AND call_id = $3
		  AND namespace = 'git' AND tool = $4 AND arguments = $5::jsonb`,
		request.ThreadID, request.TurnID, request.CallID, request.Tool,
		string(request.Arguments)).Scan(&status, &resultJSON, &message)
	if errors.Is(err, sql.ErrNoRows) {
		return codex.ToolCallResult{}, errors.New("本地 Tool Call ID 与既有请求不一致")
	}
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
