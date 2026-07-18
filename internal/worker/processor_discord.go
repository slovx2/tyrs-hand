package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
			if projectErr := discordintegration.ProjectConversationStatus(projectCtx, p.db, jobCtx.GuildID,
				jobCtx.ThreadID, jobCtx.ConversationID, "**Codex 处理失败**\n后台已记录错误，可稍后重试或联系管理员。"); projectErr != nil {
				p.logger.Warn("投影 Discord Conversation 失败状态失败", zap.Error(projectErr))
			}
		}
	}()
	p.projectDiscordConversation(ctx, jobCtx, "**Codex 正在处理**\n已接收消息，正在准备工作区。")
	workspace, branch, err := p.ensureDiscordWorkspace(ctx, claimed, jobCtx)
	if err != nil {
		return err
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
	poolKey := "discord/" + claimed.DiscordConversationID.String() + "/" + claimed.AgentProfileID.String() + "/" + signature
	client, err := p.pool.Acquire(ctx, poolKey, workspace, codexHome, environment)
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
		p.projectDiscordConversation(ctx, jobCtx, "**需要处理**\n"+outcome.Summary)
		finalProjected = true
		return &blockedError{summary: outcome.Summary}
	}
	p.projectDiscordConversation(ctx, jobCtx, outcome.Summary)
	finalProjected = true
	return nil
}

func (p *Processor) projectDiscordConversation(ctx context.Context, jobCtx discordJobContext, content string) {
	if err := discordintegration.ProjectConversationStatus(ctx, p.db, jobCtx.GuildID,
		jobCtx.ThreadID, jobCtx.ConversationID, content); err != nil {
		p.logger.Warn("投影 Discord Conversation 状态失败", zap.Error(err),
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

var branchPart = regexp.MustCompile(`[^a-z0-9._-]+`)

func discordBranch(owner string, conversationID uuid.UUID) string {
	owner = strings.Trim(branchPart.ReplaceAllString(strings.ToLower(owner), "-"), "-.")
	if owner == "" {
		owner = "unbound"
	}
	return "discord/" + owner + "/" + shortID(conversationID)
}

func (p *Processor) ensureDiscordWorkspace(ctx context.Context, claimed *queue.ClaimedJob, jobCtx discordJobContext) (string, string, error) {
	if !jobCtx.HasRepository {
		path := filepath.Join(p.cfg.DiscordWorkspaceRoot, claimed.DiscordConversationID.String())
		if err := os.MkdirAll(path, 0o700); err != nil {
			return "", "", err
		}
		_, err := p.db.ExecContext(ctx, `INSERT INTO discord_conversation_workspaces(conversation_id, workspace_type, path)
			VALUES ($1, 'blank', $2) ON CONFLICT(conversation_id) DO UPDATE SET path = EXCLUDED.path,
			status = 'ready', error = NULL, last_used_at = now()`, claimed.DiscordConversationID, path)
		return path, "", err
	}
	credential, err := p.control.GitCredential(ctx, claimed.Capability, "fetch")
	if err != nil {
		return "", "", err
	}
	branch := discordBranch(jobCtx.GitHubLogin, claimed.DiscordConversationID)
	workspace, err := p.workspace.Ensure(ctx, ports.WorkspaceSpec{
		RepositoryID: jobCtx.RepositoryID.String(), WorkItemID: claimed.DiscordConversationID.String(),
		CloneURL: jobCtx.CloneURL, BaseRef: "refs/remotes/origin/" + jobCtx.DefaultBranch, Branch: branch,
	}, credential)
	if err != nil {
		return "", "", err
	}
	_, err = p.db.ExecContext(ctx, `INSERT INTO discord_conversation_workspaces
		(conversation_id, workspace_type, path, branch, base_sha, head_sha)
		VALUES ($1, 'worktree', $2, $3, $4, $5)
		ON CONFLICT(conversation_id) DO UPDATE SET path = EXCLUDED.path, branch = EXCLUDED.branch,
			head_sha = EXCLUDED.head_sha, status = 'ready', error = NULL, last_used_at = now()`,
		claimed.DiscordConversationID, workspace.WorktreePath, branch, jobCtx.DefaultBranch, workspace.HeadSHA)
	return workspace.WorktreePath, branch, err
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
		return codex.TextToolResult(fmt.Sprintf(`{"sha":%q}`, sha), err == nil), err
	case "publish_branch":
		credential, err := p.control.GitCredential(ctx, claimed.Capability, "push", request.ThreadID, request.TurnID)
		if err != nil {
			return codex.ToolCallResult{}, err
		}
		sha, err := p.workspace.Publish(ctx, workspace.WorktreePath, branch, credential)
		return codex.TextToolResult(fmt.Sprintf(`{"branch":%q,"sha":%q}`, branch, sha), err == nil), err
	default:
		return codex.ToolCallResult{}, fmt.Errorf("本地 Git 工具 %s 未授权", request.Tool)
	}
}
