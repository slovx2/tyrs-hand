package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/slovx2/tyrs-hand/internal/domain"
	"github.com/slovx2/tyrs-hand/internal/ports"
	"github.com/slovx2/tyrs-hand/internal/queue"
	"go.uber.org/zap"
)

type discordJobContext struct {
	jobContext
	ConversationID uuid.UUID
	GuildID        string
	ThreadID       string
	MessageID      string
	OwnerUserID    string
	RepositoryID   uuid.UUID
	HasRepository  bool
	Body           string
	DiscordUserID  string
	DisplayName    string
	Username       string
	GitHubUserID   int64
	GitHubLogin    string
	BindingID      string
	BindingVersion int64
	Access         string
}

func (p *Processor) processDiscordConversation(ctx context.Context, claimed *queue.ClaimedJob) (processErr error) {
	jobCtx, err := p.loadDiscordContext(ctx, claimed.Job)
	if err != nil {
		return err
	}
	finalProjected := false
	defer func() {
		if processErr != nil && !finalProjected {
			projectCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			state, detail := discordFailureProjection(projectCtx, p.db, claimed.ID, processErr)
			if projectErr := discordintegration.ProjectConversationStatus(projectCtx, p.db, jobCtx.GuildID,
				jobCtx.ThreadID, jobCtx.ConversationID, jobCtx.MessageID,
				state, detail); projectErr != nil {
				p.logger.Warn("投影 Discord Conversation 失败状态失败", zap.Error(projectErr))
			}
		}
	}()
	p.projectDiscordConversation(ctx, jobCtx, discordintegration.ConversationRunning, "已接收消息，正在准备工作区。")
	workspace, branch, err := p.ensureDiscordWorkspace(ctx, claimed, jobCtx)
	if err != nil {
		return err
	}
	if jobCtx.HasRepository {
		defer p.refreshDiscordWorkspaceState(context.Background(), workspace)
	}
	environmentResult := p.prepareDiscordEnvironment(ctx, workspace)
	if environmentResult.Status == "degraded" {
		p.projectDiscordConversation(ctx, jobCtx, discordintegration.ConversationRunning,
			"工作区已创建，但开发环境未完全准备；Agent 将携带诊断继续执行。")
	}
	skills, err := resolveSkills(workspace, claimed.Skills)
	if err != nil {
		return err
	}
	provider, err := p.settings.AgentProvider(ctx)
	if err != nil {
		return err
	}
	signature := provider.ConfigSignature
	if signature == "" {
		signature = "default"
	}
	codexHome := filepath.Join(p.cfg.CodexHomeRoot, "discord", claimed.DiscordConversationID.String(), signature[:min(16, len(signature))])
	provider, environment, err := p.settings.PrepareCodexHome(ctx, codexHome, filepath.Join(p.cfg.CodexHomeRoot, "shared"))
	if err != nil {
		return err
	}
	environment = append(environment, environmentResult.Environment...)
	poolKey := "job/" + claimed.ID.String()
	client, err := p.pool.Acquire(ctx, poolKey, workspace, codexHome, environment)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := p.pool.Release(poolKey); closeErr != nil {
			p.logger.Warn("关闭 Discord Job Codex App Server 失败", zap.Error(closeErr), zap.String("job_id", claimed.ID.String()))
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
		CWD: workspace, Model: jobCtx.Model, ReasoningEffort: jobCtx.ReasoningEffort,
		ServiceTier: jobCtx.ServiceTier, Sandbox: jobCtx.Sandbox, ApprovalPolicy: jobCtx.ApprovalPolicy,
		NetworkEnabled:        jobCtx.NetworkEnabled,
		DeveloperInstructions: discordintegration.MultiplayerDeveloperInstructions,
	}
	if jobCtx.HasRepository {
		githubSpec, specErr := p.catalog.DynamicToolSpecFor(append(append([]string{}, claimed.AllowedTools...), claimed.DangerousActions...))
		if specErr != nil {
			return specErr
		}
		options.DynamicTools = []ports.DynamicToolSpec{githubSpec, localGitSpec()}
		options.DeveloperInstructions += "\nFollow repository AGENTS.md and the explicitly attached skills. Use only the selected repository and managed Discord worktree. Use git.commit and git.publish_branch for writes."
	}
	if err := runtime.ValidateSkills(ctx, workspace, skills); err != nil {
		return err
	}
	threadSignature := threadConfigSignature(signature, options)
	threadDBID, threadID, err := p.ensureDiscordThread(ctx, runtime, claimed.Job, options, codexHome, threadSignature)
	if err != nil {
		return err
	}
	portWorkspace := ports.Workspace{WorktreePath: workspace, Branch: branch}
	unbind, err := p.pool.Bind(poolKey, threadID, func(toolCtx context.Context, request codex.ToolCallRequest) (codex.ToolCallResult, error) {
		return p.handleDiscordTool(toolCtx, claimed, portWorkspace, branch, request)
	})
	if err != nil {
		return err
	}
	defer unbind()
	input, err := p.discordTurnInput(ctx, jobCtx, workspace, skills)
	if err != nil {
		return err
	}
	input.AdditionalContext = mergeAdditionalContext(input.AdditionalContext, environmentAdditionalContext(environmentResult))
	turnID, err := runtime.StartTurn(ctx, threadID, input)
	if err != nil {
		return err
	}
	if err := p.addDiscordContributor(ctx, claimed.DiscordConversationID, turnID, claimed.DiscordMessageID); err != nil {
		_ = runtime.InterruptTurn(context.Background(), threadID, turnID)
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
	if err := p.persistDiscordSummary(ctx, claimed.DiscordConversationID, threadID, outcome.Summary); err != nil {
		p.logger.Warn("持久化 Discord Conversation Summary 失败", zap.Error(err), zap.String("conversation_id", claimed.DiscordConversationID.String()))
	}
	_, err = p.db.ExecContext(ctx, `UPDATE agent_threads SET last_turn_id = $2, last_used_at = now(),
		expires_at = now() + interval '30 days' WHERE id = $1`, threadDBID, turnID)
	if err == nil {
		_, err = p.db.ExecContext(ctx, `UPDATE discord_input_messages SET status = 'processed', processed_at = now()
			WHERE message_id = $1`, claimed.DiscordMessageID)
	}
	if err != nil {
		return err
	}
	if outcome.Status == "blocked" {
		p.projectDiscordConversation(ctx, jobCtx, discordintegration.ConversationBlocked, "本轮需要你进一步处理。")
		p.projectDiscordReply(ctx, jobCtx, outcome.Summary)
		finalProjected = true
		return &blockedError{summary: outcome.Summary}
	}
	p.projectDiscordConversation(ctx, jobCtx, discordintegration.ConversationCompleted, "本轮处理完成。")
	p.projectDiscordReply(ctx, jobCtx, outcome.Summary)
	finalProjected = true
	return nil
}

func discordFailureProjection(ctx context.Context, db *sql.DB, jobID uuid.UUID,
	cause error,
) (discordintegration.ConversationProgress, string) {
	if discordStopRequested(ctx, db, jobID, cause) {
		return discordintegration.ConversationCanceled, "本轮已由 Discord 用户主动停止。"
	}
	return discordintegration.ConversationFailed, "后台已记录错误，可稍后重试或联系管理员。"
}

func (p *Processor) projectDiscordConversation(ctx context.Context, jobCtx discordJobContext,
	state discordintegration.ConversationProgress, detail string,
) {
	if err := discordintegration.ProjectConversationStatus(ctx, p.db, jobCtx.GuildID,
		jobCtx.ThreadID, jobCtx.ConversationID, jobCtx.MessageID, state, detail); err != nil {
		p.logger.Warn("投影 Discord Conversation 状态失败", zap.Error(err),
			zap.String("conversation_id", jobCtx.ConversationID.String()))
	}
}

func (p *Processor) projectDiscordReply(ctx context.Context, jobCtx discordJobContext, content string) {
	if err := discordintegration.ProjectConversationReply(ctx, p.db, jobCtx.ThreadID,
		jobCtx.ConversationID, jobCtx.MessageID, content); err != nil {
		p.logger.Warn("投影 Discord Conversation 最终回复失败", zap.Error(err),
			zap.String("conversation_id", jobCtx.ConversationID.String()))
	}
}

func (p *Processor) loadDiscordContext(ctx context.Context, job domain.Job) (discordJobContext, error) {
	var result discordJobContext
	var repositoryID sql.NullString
	err := p.db.QueryRowContext(ctx, `SELECT c.id, c.guild_id, c.thread_id, m.message_id, c.owner_discord_user_id,
		COALESCE(c.repository_id::text, ''), COALESCE(r.owner, ''), COALESCE(r.name, ''),
		COALESCE(r.clone_url, ''), COALESCE(r.default_branch, ''), c.context_version,
		p.name, COALESCE(p.model, ''), COALESCE(p.reasoning_effort, ''), COALESCE(p.service_tier, ''),
		p.sandbox, p.approval_policy, p.network_enabled, m.body, m.discord_user_id,
		m.display_name, m.username, COALESCE(m.github_user_id, 0), COALESCE(m.github_login, ''),
		COALESCE(m.github_binding_id::text, ''), COALESCE(m.binding_version, 0), m.access_snapshot
		FROM discord_conversations c JOIN discord_input_messages m ON m.conversation_id = c.id
		JOIN agent_profiles p ON p.id = c.agent_profile_id
		LEFT JOIN repositories r ON r.id = c.repository_id
		WHERE c.id = $1 AND m.message_id = $2`, job.DiscordConversationID, job.DiscordMessageID).
		Scan(&result.ConversationID, &result.GuildID, &result.ThreadID, &result.MessageID, &result.OwnerUserID,
			&repositoryID, &result.Owner, &result.Repository, &result.CloneURL, &result.DefaultBranch,
			&result.ContextVersion, &result.ProfileName, &result.Model, &result.ReasoningEffort,
			&result.ServiceTier, &result.Sandbox, &result.ApprovalPolicy, &result.NetworkEnabled,
			&result.Body, &result.DiscordUserID, &result.DisplayName, &result.Username,
			&result.GitHubUserID, &result.GitHubLogin, &result.BindingID, &result.BindingVersion, &result.Access)
	if err != nil {
		return discordJobContext{}, err
	}
	if repositoryID.String != "" {
		result.RepositoryID, err = uuid.Parse(repositoryID.String)
		result.HasRepository = err == nil
	}
	return result, err
}

func (p *Processor) ensureDiscordWorkspace(ctx context.Context, claimed *queue.ClaimedJob, jobCtx discordJobContext) (string, string, error) {
	if !jobCtx.HasRepository {
		path := filepath.Join(p.cfg.DiscordWorkspaceRoot, "blank", claimed.DiscordConversationID.String())
		if err := os.MkdirAll(path, 0o700); err != nil {
			return "", "", err
		}
		return path, "", nil
	}
	credential, err := p.control.GitCredential(ctx, claimed.Capability, "fetch")
	if err != nil {
		return "", "", err
	}
	workspaceID, workspacePath, branch, err := p.bindDiscordWorkspace(ctx, jobCtx)
	if err != nil {
		return "", "", err
	}
	workspace, err := p.workspace.Ensure(ctx, ports.WorkspaceSpec{
		RepositoryID: jobCtx.RepositoryID.String(), WorkItemID: workspaceID.String(),
		WorktreePath: workspacePath, CloneURL: jobCtx.CloneURL,
		BaseRef: "refs/remotes/origin/" + jobCtx.DefaultBranch, Branch: branch,
	}, credential)
	if err != nil {
		_, _ = p.db.ExecContext(context.Background(), `UPDATE discord_workspaces SET status = 'error',
			error = $2, updated_at = now() WHERE id = $1`, workspaceID, err.Error())
		return "", "", err
	}
	var cacheID uuid.UUID
	err = p.db.QueryRowContext(ctx, `INSERT INTO repo_caches(repository_id, path, status, last_fetch_at)
		VALUES ($1, $2, 'ready', now())
		ON CONFLICT(repository_id) DO UPDATE SET path = EXCLUDED.path, status = 'ready',
			error = NULL, last_fetch_at = now(), last_used_at = now()
		RETURNING id`, jobCtx.RepositoryID, workspace.CachePath).Scan(&cacheID)
	if err == nil {
		_, err = p.db.ExecContext(ctx, `UPDATE discord_workspaces SET repo_cache_id = $2,
			path = $3, branch = $4, base_sha = COALESCE(base_sha, $5), head_sha = $5,
			status = 'ready', error = NULL, last_used_at = now(), updated_at = now()
			WHERE id = $1`, workspaceID, cacheID, workspace.WorktreePath, branch, workspace.HeadSHA)
	}
	return workspace.WorktreePath, branch, err
}

func (p *Processor) bindDiscordWorkspace(ctx context.Context, jobCtx discordJobContext) (uuid.UUID, string, string, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return uuid.Nil, "", "", err
	}
	defer func() { _ = tx.Rollback() }()

	candidateID := uuid.New()
	candidatePath := filepath.Join(p.cfg.DiscordWorkspaceRoot, candidateID.String())
	candidateBranch := "tyrs-hand/discord/" + candidateID.String()
	var workspaceID uuid.UUID
	var path, branch string
	err = tx.QueryRowContext(ctx, `INSERT INTO discord_workspaces
		(id, guild_id, owner_discord_user_id, repository_id, name, path, branch)
		VALUES ($1, $2, $3, $4, 'default', $5, $6)
		ON CONFLICT(guild_id, owner_discord_user_id, repository_id, name)
		DO UPDATE SET last_used_at = now(), updated_at = now()
		RETURNING id, path, branch`, candidateID, jobCtx.GuildID, jobCtx.OwnerUserID,
		jobCtx.RepositoryID, candidatePath, candidateBranch).Scan(&workspaceID, &path, &branch)
	if err != nil {
		return uuid.Nil, "", "", err
	}
	result, err := tx.ExecContext(ctx, `UPDATE discord_conversations SET workspace_id = $2, updated_at = now()
		WHERE id = $1 AND repository_id = $3`, jobCtx.ConversationID, workspaceID, jobCtx.RepositoryID)
	if err != nil {
		return uuid.Nil, "", "", err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return uuid.Nil, "", "", errors.New("会话的仓库绑定已经变化")
	}
	if err := tx.Commit(); err != nil {
		return uuid.Nil, "", "", err
	}
	return workspaceID, path, branch, nil
}

func (p *Processor) handleDiscordTool(ctx context.Context, claimed *queue.ClaimedJob, workspace ports.Workspace, branch string, request codex.ToolCallRequest) (codex.ToolCallResult, error) {
	if request.Namespace != nil && *request.Namespace == "github" {
		return p.control.CallTool(ctx, claimed.Capability, request)
	}
	if request.Namespace == nil || *request.Namespace != "git" {
		return codex.ToolCallResult{}, errors.New("未知 dynamic tool namespace")
	}
	return p.auditLocalToolCall(ctx, claimed, request, func() (codex.ToolCallResult, error) {
		return p.executeDiscordLocalTool(ctx, claimed, workspace, branch, request)
	})
}

func (p *Processor) executeDiscordLocalTool(ctx context.Context, claimed *queue.ClaimedJob, workspace ports.Workspace, branch string, request codex.ToolCallRequest) (codex.ToolCallResult, error) {
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
			_, err = p.db.ExecContext(ctx, `UPDATE discord_workspaces SET head_sha = $2, dirty = false,
				last_used_at = now(), updated_at = now() WHERE path = $1`, workspace.WorktreePath, sha)
		}
		return codex.TextToolResult(fmt.Sprintf(`{"sha":%q}`, sha), err == nil), err
	case "publish_branch":
		credential, err := p.control.GitCredential(ctx, claimed.Capability, "push", request.ThreadID, request.TurnID)
		if err != nil {
			return codex.ToolCallResult{}, err
		}
		sha, err := p.workspace.Publish(ctx, workspace.WorktreePath, branch, credential)
		if err == nil {
			_, err = p.db.ExecContext(ctx, `UPDATE discord_workspaces SET head_sha = $2,
				last_used_at = now(), updated_at = now() WHERE path = $1`, workspace.WorktreePath, sha)
		}
		return codex.TextToolResult(fmt.Sprintf(`{"branch":%q,"sha":%q}`, branch, sha), err == nil), err
	default:
		return codex.ToolCallResult{}, fmt.Errorf("本地 Git 工具 %s 未授权", request.Tool)
	}
}

func (p *Processor) refreshDiscordWorkspaceState(parent context.Context, path string) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	status, err := p.workspace.Status(ctx, path)
	if err != nil {
		_, _ = p.db.ExecContext(ctx, `UPDATE discord_workspaces SET status = 'error', error = $2,
			last_used_at = now(), updated_at = now() WHERE path = $1`, path, err.Error())
		return
	}
	dirty := false
	for _, line := range strings.Split(strings.TrimSpace(status), "\n") {
		if line != "" && !strings.HasPrefix(line, "##") {
			dirty = true
			break
		}
	}
	_, _ = p.db.ExecContext(ctx, `UPDATE discord_workspaces SET dirty = $2, status = 'ready',
		error = NULL, last_used_at = now(), updated_at = now() WHERE path = $1`, path, dirty)
}
