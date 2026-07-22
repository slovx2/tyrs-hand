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
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/codexsettings"
	"github.com/slovx2/tyrs-hand/internal/devcontainer"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/slovx2/tyrs-hand/internal/ports"
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
	EnvironmentID  uuid.UUID
	ForumID        uuid.UUID
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

func (p *Processor) processDiscordConversation(ctx context.Context,
	claimed *codexcontrol.ClaimedControl,
) (result codexcontrol.TurnResult, processErr error) {
	jobCtx, err := p.loadDiscordContext(ctx, claimed.Intent)
	if err != nil {
		return result, err
	}
	defer cleanupBrowserTask(p.cfg, claimed.ID.String())
	preferences, err := p.freezeRuntimePreferences(ctx, claimed)
	if err != nil {
		return result, err
	}
	jobCtx.Model = preferences.Model
	jobCtx.ReasoningEffort = preferences.ReasoningEffort
	jobCtx.ServiceTier = codexsettings.RuntimeServiceTier(preferences.ServiceTier)
	progress := p.newDiscordProgressReporter(ctx, claimed, jobCtx)
	finalProjected := false
	defer func() {
		if processErr != nil && !finalProjected {
			projectCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			state, detail := discordFailureProjection(projectCtx, p.db, claimed.ID, processErr)
			if projectErr := discordintegration.ProjectConversationStatus(projectCtx, p.db, jobCtx.GuildID,
				jobCtx.ThreadID, jobCtx.ConversationID, jobCtx.MessageID, claimed.RunID,
				state, detail); projectErr != nil {
				p.logger.Warn("投影 Discord Conversation 失败状态失败", zap.Error(projectErr))
			}
		}
	}()
	progress.project(ctx, discordintegration.ConversationRunning, "已接收消息，正在准备工作区。", 0)
	credential, err := p.control.GitCredential(ctx, claimed.Capability, "fetch")
	if err != nil {
		return result, err
	}
	containerRuntime, err := p.development.Ensure(ctx, jobCtx.EnvironmentID, jobCtx.ForumID,
		jobCtx.ConversationID, credential)
	if err != nil {
		return result, err
	}
	defer p.development.MarkIdle(context.Background(), jobCtx.EnvironmentID)
	defer func() {
		refreshCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		p.refreshDiscordWorkspaceState(refreshCtx, containerRuntime)
	}()
	workspace := containerRuntime.Workspace
	skills, err := resolveContainerSkills(workspace, claimed.Skills)
	if err != nil {
		return result, err
	}
	provider, err := p.settings.AgentProvider(ctx)
	if err != nil {
		return result, err
	}
	signature := provider.ConfigSignature
	if signature == "" {
		signature = "default"
	}
	if err := os.MkdirAll(filepath.Join(p.cfg.WorkerDataRoot, "tmp"), 0o750); err != nil {
		return result, err
	}
	temporaryHome, err := os.MkdirTemp(filepath.Join(p.cfg.WorkerDataRoot, "tmp"), "discord-codex-home-*")
	if err != nil {
		return result, err
	}
	defer func() { _ = os.RemoveAll(temporaryHome) }()
	provider, environment, err := p.settings.PrepareCodexHome(ctx, temporaryHome, filepath.Join(p.cfg.CodexHomeRoot, "shared"))
	if err != nil {
		return result, err
	}
	environment, runtimeConfig := prepareCodexRuntime(environment, "", p.cfg)
	if err := p.development.CopyToRuntime(ctx, containerRuntime, temporaryHome, containerRuntime.CodexHome); err != nil {
		return result, err
	}
	poolKey := "job/" + claimed.ID.String()
	client, err := p.pool.AcquireWithLauncher(ctx, poolKey, workspace, containerRuntime.CodexHome, containerRuntime.Home,
		environment, p.development.Launcher(containerRuntime), "/opt/tyrs-hand/bin/codex")
	if err != nil {
		return result, err
	}
	defer func() {
		if closeErr := p.pool.Release(poolKey); closeErr != nil {
			p.logger.Warn("关闭 Discord Job Codex App Server 失败", zap.Error(closeErr), zap.String("job_id", claimed.ID.String()))
		}
	}()
	runtime := codex.NewRuntime(client)
	options := ports.ThreadOptions{
		CWD: workspace, Model: jobCtx.Model, ReasoningEffort: jobCtx.ReasoningEffort,
		ServiceTier: jobCtx.ServiceTier, Sandbox: "danger-full-access", ApprovalPolicy: jobCtx.ApprovalPolicy,
		NetworkEnabled:        jobCtx.NetworkEnabled,
		RuntimeConfig:         runtimeConfig,
		DeveloperInstructions: browserDeveloperInstructions(p.cfg, discordintegration.MultiplayerDeveloperInstructions),
	}
	if jobCtx.HasRepository {
		githubSpec, specErr := p.catalog.DynamicToolSpecFor(append(append([]string{}, claimed.AllowedTools...), claimed.DangerousActions...))
		if specErr != nil {
			return result, specErr
		}
		options.DynamicTools = append(options.DynamicTools, githubSpec, localGitSpec())
		options.DeveloperInstructions += "\nFollow repository AGENTS.md and the explicitly attached skills. Use only the selected repository and persistent Discord clone. The container and Home are shared with the owner's other forums, so never inspect or modify sibling workspaces outside the current CWD. Use git.commit and git.publish_branch for writes."
	}
	options.DynamicTools = withBrowserTools(p.cfg, options.DynamicTools...)
	if err := runtime.ValidateSkills(ctx, workspace, skills); err != nil {
		return result, err
	}
	threadSignature := threadConfigSignature(signature, options)
	threadID, err := p.ensureThread(ctx, runtime, claimed, options, containerRuntime.CodexHome, threadSignature)
	if err != nil {
		return result, err
	}
	portWorkspace := ports.Workspace{WorktreePath: workspace}
	unbind, err := p.pool.Bind(poolKey, threadID, func(toolCtx context.Context, request codex.ToolCallRequest) (codex.ToolCallResult, error) {
		progress.dynamicTool(request, "running")
		toolResult, toolErr := p.handleDiscordTool(toolCtx, claimed, containerRuntime, portWorkspace, request)
		state := "completed"
		if toolErr != nil || !toolResult.Success {
			state = "failed"
		}
		progress.dynamicTool(request, state)
		return toolResult, toolErr
	})
	if err != nil {
		return result, err
	}
	defer unbind()
	if claimed.Recovering {
		var recovered bool
		result, recovered, err = p.reconcileTurn(ctx, runtime, claimed, threadID, progress.observeEvent)
		if err != nil {
			return result, err
		}
		if recovered {
			progress.project(ctx, discordintegration.ConversationCompleted, "本轮处理完成。", result.DurationMillis)
			p.projectDiscordReply(ctx, jobCtx, result.FinalAnswer)
			p.projectDiscordRunContributors(ctx, claimed.RunID, claimed.DiscordMessageID,
				result.FinalAnswer, progress.detail("本轮处理完成。", result.DurationMillis))
			finalProjected = true
			return result, nil
		}
	}
	input, err := p.discordTurnInput(ctx, jobCtx, workspace, skills)
	if err != nil {
		return result, err
	}
	turnID, err := runtime.StartTurn(ctx, threadID, input)
	if err != nil {
		return result, err
	}
	if err := p.controls.RecordSubmission(ctx, claimed, turnID); err != nil {
		return result, err
	}
	if err := p.addDiscordContributor(ctx, claimed.RunID, claimed.DiscordConversationID, turnID, claimed.DiscordMessageID); err != nil {
		interruptTurnBestEffort(runtime, threadID, turnID)
		return result, err
	}
	result, err = p.waitTurn(ctx, runtime, client.Events(), claimed, threadID, turnID, progress.observeEvent)
	if err != nil {
		if needsCleanupInterrupt(err) {
			interruptTurnBestEffort(runtime, threadID, turnID)
		}
		return result, err
	}
	_, err = p.db.ExecContext(ctx, `UPDATE discord_input_messages SET status = 'processed', processed_at = now()
		WHERE message_id = $1`, claimed.DiscordMessageID)
	if err != nil {
		return result, err
	}
	progress.project(ctx, discordintegration.ConversationCompleted, "本轮处理完成。", result.DurationMillis)
	p.projectDiscordReply(ctx, jobCtx, result.FinalAnswer)
	p.projectDiscordRunContributors(ctx, claimed.RunID, claimed.DiscordMessageID,
		result.FinalAnswer, progress.detail("本轮处理完成。", result.DurationMillis))
	finalProjected = true
	return result, nil
}

func (p *Processor) projectDiscordRunContributors(ctx context.Context, runID uuid.UUID,
	primaryMessageID, finalAnswer, detail string,
) {
	rows, err := p.db.QueryContext(ctx, `SELECT i.discord_conversation_id, i.discord_message_id
		FROM codex_turn_intents i JOIN codex_turn_runs r ON r.control_id = i.control_id
		WHERE r.id = $1 AND i.resolved_action = 'steer' AND i.status = 'running'
		  AND i.discord_message_id <> $2 ORDER BY i.sequence_no`, runID, primaryMessageID)
	if err != nil {
		p.logger.Warn("读取 Discord Turn Contributors 失败", zap.Error(err))
		return
	}
	type contributor struct {
		conversationID uuid.UUID
		messageID      string
	}
	var contributors []contributor
	for rows.Next() {
		var item contributor
		if rows.Scan(&item.conversationID, &item.messageID) == nil {
			contributors = append(contributors, item)
		}
	}
	_ = rows.Close()
	for _, item := range contributors {
		jobCtx, loadErr := p.loadDiscordContext(ctx, codexcontrol.Intent{
			DiscordConversationID: item.conversationID, DiscordMessageID: item.messageID,
		})
		if loadErr != nil {
			p.logger.Warn("加载 Discord Contributor 消息失败", zap.Error(loadErr))
			continue
		}
		p.projectDiscordConversation(ctx, jobCtx, runID, discordintegration.ConversationCompleted, detail)
		p.projectDiscordReply(ctx, jobCtx, finalAnswer)
		_, _ = p.db.ExecContext(ctx, `UPDATE discord_input_messages SET status = 'processed',
			processed_at = now() WHERE message_id = $1`, item.messageID)
	}
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
	runID uuid.UUID, state discordintegration.ConversationProgress, detail string,
) {
	if err := discordintegration.ProjectConversationStatus(ctx, p.db, jobCtx.GuildID,
		jobCtx.ThreadID, jobCtx.ConversationID, jobCtx.MessageID, runID, state, detail); err != nil {
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

func (p *Processor) loadDiscordContext(ctx context.Context, job codexcontrol.Intent) (discordJobContext, error) {
	var result discordJobContext
	var repositoryID sql.NullString
	err := p.db.QueryRowContext(ctx, `SELECT c.id, c.guild_id, c.thread_id, m.message_id, c.owner_discord_user_id,
		f.id, f.development_environment_id,
		COALESCE(c.repository_id::text, ''), COALESCE(r.owner, ''), COALESCE(r.name, ''),
		COALESCE(r.clone_url, ''), COALESCE(r.default_branch, ''), c.context_version,
		p.name, COALESCE(p.model, ''), COALESCE(p.reasoning_effort, ''), COALESCE(p.service_tier, ''),
		p.sandbox, p.approval_policy, p.network_enabled, m.body, m.discord_user_id,
		m.display_name, m.username, COALESCE(m.github_user_id, 0), COALESCE(m.github_login, ''),
		COALESCE(m.github_binding_id::text, ''), COALESCE(m.binding_version, 0), m.access_snapshot
		FROM discord_conversations c JOIN discord_input_messages m ON m.conversation_id = c.id
		JOIN discord_forums f ON f.id = c.forum_id AND f.forum_type = 'development'
		JOIN agent_profiles p ON p.id = c.agent_profile_id
		LEFT JOIN repositories r ON r.id = c.repository_id
		WHERE c.id = $1 AND m.message_id = $2`, job.DiscordConversationID, job.DiscordMessageID).
		Scan(&result.ConversationID, &result.GuildID, &result.ThreadID, &result.MessageID, &result.OwnerUserID,
			&result.ForumID, &result.EnvironmentID,
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

func (p *Processor) handleDiscordTool(ctx context.Context, claimed *codexcontrol.ClaimedControl,
	runtime devcontainer.Runtime, workspace ports.Workspace, request codex.ToolCallRequest,
) (codex.ToolCallResult, error) {
	if request.Namespace != nil && *request.Namespace == "github" {
		return p.control.CallTool(ctx, claimed.Capability, request)
	}
	if request.Namespace != nil && *request.Namespace == browserToolNamespace {
		return p.auditLocalToolCall(ctx, claimed, request, func() (codex.ToolCallResult, error) {
			return executeBrowserTool(ctx, p.cfg, claimed.ID.String(), runtime.Workspace,
				&runtime, p.development, request)
		})
	}
	if request.Namespace == nil || *request.Namespace != "git" {
		return codex.ToolCallResult{}, errors.New("未知 dynamic tool namespace")
	}
	return p.auditLocalToolCall(ctx, claimed, request, func() (codex.ToolCallResult, error) {
		return p.executeDiscordLocalTool(ctx, claimed, runtime, workspace, request)
	})
}

func (p *Processor) executeDiscordLocalTool(ctx context.Context, claimed *codexcontrol.ClaimedControl,
	runtime devcontainer.Runtime, workspace ports.Workspace, request codex.ToolCallRequest,
) (codex.ToolCallResult, error) {
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
		if err == nil {
			_, err = p.db.ExecContext(ctx, `UPDATE discord_forum_workspaces SET head_sha = $2, dirty = false,
				last_used_at = now(), updated_at = now() WHERE forum_id = $1`, runtime.ForumID, sha)
		}
		return codex.TextToolResult(fmt.Sprintf(`{"sha":%q}`, sha), err == nil), err
	case "publish_branch":
		credential, err := p.control.GitCredential(ctx, claimed.Capability, "push", request.ThreadID, request.TurnID)
		if err != nil {
			return codex.ToolCallResult{}, err
		}
		branch, sha, err := p.development.Publish(ctx, runtime, credential)
		if err == nil {
			_, err = p.db.ExecContext(ctx, `UPDATE discord_forum_workspaces SET base_sha = $2, head_sha = $2,
				last_used_at = now(), updated_at = now() WHERE forum_id = $1`, runtime.ForumID, sha)
		}
		return codex.TextToolResult(fmt.Sprintf(`{"branch":%q,"sha":%q}`, branch, sha), err == nil), err
	default:
		return codex.ToolCallResult{}, fmt.Errorf("本地 Git 工具 %s 未授权", request.Tool)
	}
}

func (p *Processor) refreshDiscordWorkspaceState(ctx context.Context, runtime devcontainer.Runtime) {
	status, statusErr := p.development.Git(ctx, runtime, "status", "--porcelain=v1")
	head, headErr := p.development.Git(ctx, runtime, "rev-parse", "HEAD")
	if statusErr != nil || headErr != nil {
		cause := statusErr
		if cause == nil {
			cause = headErr
		}
		_, _ = p.db.ExecContext(ctx, `UPDATE discord_forum_workspaces
			SET error = $2, updated_at = now() WHERE forum_id = $1`, runtime.ForumID, cause.Error())
		return
	}
	_, _ = p.db.ExecContext(ctx, `UPDATE discord_forum_workspaces SET head_sha = $2, dirty = $3,
		error = NULL, last_used_at = now(), updated_at = now() WHERE forum_id = $1`,
		runtime.ForumID, strings.TrimSpace(head), strings.TrimSpace(status) != "")
}
