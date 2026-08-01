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
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"go.uber.org/zap"
)

type discordJobContext struct {
	jobContext
	IntentID       uuid.UUID
	ConversationID uuid.UUID
	GuildID        string
	ThreadID       string
	MessageID      string
	ReplyMessageID string
	ProjectionID   string
	OwnerUserID    string
	ProjectID      uuid.UUID
	EnvironmentID  uuid.UUID
	ForumID        uuid.UUID
	ProjectKind    string
	HasRemote      bool
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
	jobCtx.ReplyMessageID = jobCtx.MessageID
	jobCtx.ProjectionID = claimed.ProjectionAnchor
	if jobCtx.ProjectionID == "" {
		jobCtx.ProjectionID = jobCtx.MessageID
	}
	if claimed.Operation == "replace_last_turn" {
		_ = p.db.QueryRowContext(ctx, `SELECT COALESCE(discord_message_id,$2)
			FROM codex_turn_intents WHERE id=$1`, claimed.TargetIntentID, jobCtx.MessageID).
			Scan(&jobCtx.ReplyMessageID)
	}
	defer cleanupBrowserTask(p.cfg, claimed.ID.String(), jobCtx.EnvironmentID.String())
	if claimed.Operation == "replace_last_turn" {
		defer func() {
			if processErr != nil {
				releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				p.releaseTerminalReplacement(releaseCtx, claimed, processErr)
			}
		}()
	}
	preferences, err := p.freezeRuntimePreferences(ctx, claimed)
	if err != nil {
		return result, err
	}
	jobCtx.Model = preferences.Model
	jobCtx.ReasoningEffort = preferences.ReasoningEffort
	jobCtx.ServiceTier = codexsettings.RuntimeServiceTier(preferences.ServiceTier)
	progress := p.newDiscordProgressReporter(ctx, claimed, jobCtx)
	if claimed.Operation == "replace_last_turn" {
		progress.project(ctx, discordintegration.ConversationRunning,
			"消息已编辑，正在重新生成。", 0)
		if jobCtx.MessageID != jobCtx.ProjectionID {
			tx, txErr := p.db.BeginTx(ctx, nil)
			if txErr == nil {
				txErr = discordintegration.ProjectConversationThinkingTx(ctx, tx, jobCtx.GuildID,
					jobCtx.ThreadID, jobCtx.ConversationID, jobCtx.MessageID)
			}
			if txErr == nil {
				txErr = discordintegration.RegisterConversationStatusSteerTx(ctx, tx,
					claimed.RunID, jobCtx.ConversationID, jobCtx.GuildID, jobCtx.MessageID)
			}
			if txErr == nil {
				txErr = tx.Commit()
			} else if tx != nil {
				_ = tx.Rollback()
			}
			if txErr != nil {
				p.logger.Warn("移动 replacement 进度卡失败", zap.Error(txErr))
			}
		}
		var hasReply bool
		_ = p.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM discord_projections
			WHERE projection_key=$1)`, "conversation-reply:"+jobCtx.ConversationID.String()+
			":message:"+jobCtx.ProjectionID).Scan(&hasReply)
		if hasReply {
			if err := discordintegration.ProjectConversationReplyRegenerating(ctx, p.db,
				jobCtx.ThreadID, jobCtx.ConversationID, jobCtx.ProjectionID); err != nil {
				p.logger.Warn("失效 replacement 旧结果失败", zap.Error(err))
			}
		}
	}
	finalProjected := false
	defer func() {
		if processErr != nil && !finalProjected {
			projectCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			state, detail, errorDetails := discordFailureProjection(projectCtx, p.db, claimed.ID, processErr)
			if projectErr := discordintegration.ProjectConversationStatus(projectCtx, p.db, jobCtx.GuildID,
				jobCtx.ThreadID, jobCtx.ConversationID, jobCtx.MessageID, claimed.RunID,
				state, detail, errorDetails); projectErr != nil {
				p.logger.Warn("投影 Discord Conversation 失败状态失败", zap.Error(projectErr))
			}
		}
	}()
	containerRuntime, err := p.development.Ensure(ctx, jobCtx.EnvironmentID, jobCtx.ForumID,
		jobCtx.ConversationID, "")
	if err != nil {
		return result, err
	}
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
	environment, runtimeConfig := prepareCodexRuntime(environment, "", p.cfg,
		jobCtx.EnvironmentID.String(), claimed.ID.String())
	applyModelProviderConfig(runtimeConfig, provider.ModelSource, provider.BaseURL)
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
	options := workerThreadOptions(ports.ThreadOptions{
		CWD: workspace, Model: jobCtx.Model, ReasoningEffort: jobCtx.ReasoningEffort,
		ServiceTier:           jobCtx.ServiceTier,
		NetworkEnabled:        jobCtx.NetworkEnabled,
		RuntimeConfig:         runtimeConfig,
		DeveloperInstructions: browserDeveloperInstructions(p.cfg, discordintegration.MultiplayerDeveloperInstructions),
	})
	if jobCtx.ProjectKind == "git" {
		options.DynamicTools = append(options.DynamicTools, localGitSpec(jobCtx.HasRemote))
		options.DeveloperInstructions += "\nFollow repository AGENTS.md and the explicitly attached skills. Use only the selected persistent project. The container and Home are shared with the owner's other forums, so never inspect or modify sibling workspaces outside the current CWD. Use git.commit for commits and git.publish_branch only when it is available."
	} else {
		options.DeveloperInstructions += "\nUse only the current persistent project directory. The container and Home are shared with the owner's other forums, so never inspect or modify sibling workspaces outside the current CWD. This directory is not a Git repository, so Git tools are unavailable."
	}
	options.DynamicTools = withBrowserTools(p.cfg, options.DynamicTools...)
	if err := runtime.ValidateSkills(ctx, workspace,
		withBuiltinSkills(containerRuntime.CodexHome, skills)); err != nil {
		return result, err
	}
	threadPhase := "thread/start"
	if claimed.ExternalThreadID != "" {
		threadPhase = "thread/resume"
	}
	threadID, err := p.ensureThread(ctx, runtime, claimed, options,
		containerRuntime.EnvironmentID.String())
	if err != nil {
		return result, err
	}
	if err := p.recordLocalRuntimeSettingsApplied(ctx, claimed, threadPhase,
		jobCtx.Model, jobCtx.ReasoningEffort, string(jobCtx.ServiceTier)); err != nil {
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
	if claimed.Operation == "replace_last_turn" && !claimed.Recovering {
		if err := p.prepareDiscordReplacement(ctx, runtime, claimed, threadID); err != nil {
			if errors.Is(err, errReplacementSuperseded) {
				_ = discordintegration.NewSQLoutbox(p.db).Enqueue(ctx,
					"replacement-rejected:"+claimed.ID.String(), "message.create",
					"channels/"+jobCtx.ThreadID+"/messages", map[string]any{
						"channelId": jobCtx.ThreadID,
						"content":   discordintegration.EarlierMessageEditNotice,
					}, "replacement-rejected-"+claimed.ID.String())
			}
			return result, err
		}
	}
	if claimed.Recovering {
		var recovered bool
		result, recovered, err = p.reconcileTurn(ctx, runtime, claimed, threadID, progress.observeEvent)
		if err != nil {
			return result, err
		}
		if recovered {
			if expireErr := discordintegration.ExpireConversationPlanCards(ctx, p.db,
				jobCtx.ConversationID, claimed.RunID); expireErr != nil {
				p.logger.Warn("失效旧 Plan 卡片失败", zap.Error(expireErr))
			}
			if claimed.Operation == "replace_last_turn" {
				_ = p.setReplacementPhase(ctx, claimed.ID, "terminal", "")
			}
			progress.project(ctx, discordintegration.ConversationCompleted, "本轮处理完成。", result.DurationMillis)
			p.projectDiscordReply(ctx, jobCtx, claimed.RunID, result.FinalAnswer,
				result.FinalOutputType)
			p.projectDiscordRunContributors(ctx, claimed.RunID, claimed.DiscordMessageID,
				result.FinalAnswer, result.FinalOutputType,
				progress.detail("本轮处理完成。", result.DurationMillis))
			finalProjected = true
			return result, nil
		}
	}
	input, err := p.discordTurnInput(ctx, jobCtx, workspace, skills)
	if err != nil {
		return result, err
	}
	input.CollaborationMode = &ports.CollaborationMode{Mode: claimed.CollaborationMode,
		Model: jobCtx.Model, ReasoningEffort: jobCtx.ReasoningEffort}
	if claimed.Operation == "replace_last_turn" {
		input.ClientUserMessageID = claimed.ID.String()
	}
	turnID := ""
	if claimed.Operation == "replace_last_turn" {
		if snapshot, readErr := runtime.ReadThread(ctx, threadID); readErr == nil {
			if existing, ok := snapshot.TurnByClientID(claimed.ID.String()); ok {
				turnID = existing.ID
			}
		}
	}
	if turnID == "" {
		turnID, err = runtime.StartTurn(ctx, threadID, input)
	}
	if err != nil {
		return result, err
	}
	if expireErr := discordintegration.ExpireConversationPlanCards(ctx, p.db,
		jobCtx.ConversationID, claimed.RunID); expireErr != nil {
		p.logger.Warn("失效旧 Plan 卡片失败", zap.Error(expireErr))
	}
	if err := p.recordLocalRuntimeSettingsApplied(ctx, claimed, "turn/start",
		jobCtx.Model, jobCtx.ReasoningEffort, string(jobCtx.ServiceTier)); err != nil {
		interruptTurnBestEffort(runtime, threadID, turnID)
		return result, err
	}
	if err := p.controls.RecordSubmission(ctx, claimed, turnID); err != nil {
		return result, err
	}
	if claimed.Operation == "replace_last_turn" {
		if err := p.setReplacementPhase(ctx, claimed.ID, "running", ""); err != nil {
			interruptTurnBestEffort(runtime, threadID, turnID)
			return result, err
		}
		if err := p.finalizeReplacementMessages(ctx, claimed); err != nil {
			interruptTurnBestEffort(runtime, threadID, turnID)
			return result, err
		}
	}
	if err := p.addDiscordContributor(ctx, claimed.RunID, claimed.DiscordConversationID,
		claimed.ID, turnID); err != nil {
		interruptTurnBestEffort(runtime, threadID, turnID)
		return result, err
	}
	result, err = p.waitTurn(ctx, runtime, client.Events(), claimed, threadID, turnID, progress.observeEvent)
	if err != nil {
		if errors.Is(err, errDiscordTurnReplaced) {
			progress.project(ctx, discordintegration.ConversationRunning,
				"消息已编辑，正在重新生成。", 0)
			finalProjected = true
			return codexcontrol.TurnResult{TurnID: turnID, Evidence: "replaced"}, nil
		}
		if needsCleanupInterrupt(err) {
			interruptTurnBestEffort(runtime, threadID, turnID)
		}
		return result, err
	}
	_, err = p.db.ExecContext(ctx, `UPDATE discord_input_messages SET status = 'processed',
		processed_at = now() WHERE turn_intent_id = $1`, claimed.ID)
	if err != nil {
		return result, err
	}
	progress.project(ctx, discordintegration.ConversationCompleted, "本轮处理完成。", result.DurationMillis)
	p.projectDiscordReply(ctx, jobCtx, claimed.RunID, result.FinalAnswer, result.FinalOutputType)
	p.projectDiscordRunContributors(ctx, claimed.RunID, claimed.DiscordMessageID,
		result.FinalAnswer, result.FinalOutputType,
		progress.detail("本轮处理完成。", result.DurationMillis))
	finalProjected = true
	if claimed.Operation == "replace_last_turn" {
		_ = p.setReplacementPhase(ctx, claimed.ID, "terminal", "")
	}
	return result, nil
}

func (p *Processor) recordLocalRuntimeSettingsApplied(ctx context.Context,
	claimed *codexcontrol.ClaimedControl, phase, model, effort, tier string,
) error {
	appliedTier, ok := codexsettings.AppliedServiceTier(tier)
	if !ok {
		return errors.New("runtime.settings_applied serviceTier 无效")
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT settings_revision FROM codex_turn_runs
		WHERE id = $1 FOR UPDATE`, claimed.RunID).Scan(&revision); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{
		"phase": phase, "model": model, "reasoningEffort": effort,
		"serviceTier": appliedTier, "collaborationMode": claimed.CollaborationMode,
		"settingsRevision": revision,
	})
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_events
		(control_id, intent_id, run_id, event_type, external_event_id, payload)
		VALUES ($1,$2,$3,'runtime.settings_applied',$4,$5)
		ON CONFLICT(run_id, external_event_id) WHERE run_id IS NOT NULL
			AND external_event_id IS NOT NULL DO NOTHING`, claimed.ControlID, claimed.ID,
		claimed.RunID, "local:"+phase, payload)
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE codex_turn_runs SET
			applied_model = NULLIF($2,''), applied_reasoning_effort = NULLIF($3,''),
			applied_service_tier = NULLIF($4,''), applied_collaboration_mode = $5,
			applied_settings_revision = $6, settings_applied_at = now() WHERE id = $1`,
			claimed.RunID, model, effort, appliedTier, claimed.CollaborationMode, revision)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE codex_thread_controls SET
			applied_model = NULLIF($2,''), applied_reasoning_effort = NULLIF($3,''),
			applied_service_tier = NULLIF($4,''), applied_collaboration_mode = $5,
			applied_settings_revision = $6, settings_applied_at = now(), updated_at = now()
			WHERE id = $1 AND (applied_settings_revision IS NULL
					OR applied_settings_revision <= $6)`, claimed.ControlID, model, effort, appliedTier,
			claimed.CollaborationMode, revision)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (p *Processor) projectDiscordRunContributors(ctx context.Context, runID uuid.UUID,
	primaryMessageID, finalAnswer, finalOutputType, detail string,
) {
	rows, err := p.db.QueryContext(ctx, `SELECT i.id, i.discord_conversation_id,
		i.discord_message_id, i.instruction
		FROM codex_turn_intents i JOIN codex_turn_runs r ON r.control_id = i.control_id
		WHERE r.id = $1 AND i.resolved_action = 'steer' AND i.status = 'running'
		  AND i.discord_message_id <> $2 ORDER BY i.sequence_no`, runID, primaryMessageID)
	if err != nil {
		p.logger.Warn("读取 Discord Turn Contributors 失败", zap.Error(err))
		return
	}
	type contributor struct {
		intentID       uuid.UUID
		conversationID uuid.UUID
		messageID      string
		instruction    string
	}
	var contributors []contributor
	for rows.Next() {
		var item contributor
		if rows.Scan(&item.intentID, &item.conversationID, &item.messageID,
			&item.instruction) == nil {
			contributors = append(contributors, item)
		}
	}
	_ = rows.Close()
	for _, item := range contributors {
		jobCtx, loadErr := p.loadDiscordContext(ctx, codexcontrol.Intent{
			ID: item.intentID, DiscordConversationID: item.conversationID,
			DiscordMessageID: item.messageID, Instruction: item.instruction,
		})
		if loadErr != nil {
			p.logger.Warn("加载 Discord Contributor 消息失败", zap.Error(loadErr))
			continue
		}
		p.projectDiscordConversation(ctx, jobCtx, runID, discordintegration.ConversationCompleted, detail)
		p.projectDiscordReply(ctx, jobCtx, runID, finalAnswer, finalOutputType)
		_, _ = p.db.ExecContext(ctx, `UPDATE discord_input_messages SET status = 'processed',
			processed_at = now() WHERE turn_intent_id = $1`, item.intentID)
	}
}

func discordFailureProjection(ctx context.Context, db *sql.DB, jobID uuid.UUID,
	cause error,
) (discordintegration.ConversationProgress, string, *discordintegration.ComponentErrorPayload) {
	if discordStopRequested(ctx, db, jobID, cause) {
		return discordintegration.ConversationCanceled, "本轮已由 Discord 用户主动停止。", nil
	}
	var codexErr *workerprotocol.CodexTurnError
	if errors.As(cause, &codexErr) && !codexErr.WillRetry {
		return discordintegration.ConversationFailed, "本轮处理未完成。",
			discordintegration.CodexErrorForProjection(codexErr)
	}
	return discordintegration.ConversationFailed, "本轮处理未完成。", nil
}

func (p *Processor) projectDiscordConversation(ctx context.Context, jobCtx discordJobContext,
	runID uuid.UUID, state discordintegration.ConversationProgress, detail string,
) {
	if err := discordintegration.ProjectConversationStatus(ctx, p.db, jobCtx.GuildID,
		jobCtx.ThreadID, jobCtx.ConversationID, jobCtx.ProjectionID, runID, state, detail); err != nil {
		p.logger.Warn("投影 Discord Conversation 状态失败", zap.Error(err),
			zap.String("conversation_id", jobCtx.ConversationID.String()))
	}
}

func (p *Processor) projectDiscordReply(ctx context.Context, jobCtx discordJobContext,
	runID uuid.UUID, content, finalOutputType string,
) {
	if err := discordintegration.ProjectConversationReply(ctx, p.db, jobCtx.ThreadID,
		jobCtx.ConversationID, jobCtx.ProjectionID, runID, content, finalOutputType,
		jobCtx.ReplyMessageID); err != nil {
		p.logger.Warn("投影 Discord Conversation 最终回复失败", zap.Error(err),
			zap.String("conversation_id", jobCtx.ConversationID.String()))
	}
}

func (p *Processor) loadDiscordContext(ctx context.Context, job codexcontrol.Intent) (discordJobContext, error) {
	var result discordJobContext
	result.IntentID = job.ID
	var projectID sql.NullString
	err := p.db.QueryRowContext(ctx, `SELECT c.id, c.guild_id, c.thread_id, m.message_id, c.owner_discord_user_id,
		f.id, f.development_environment_id,
		COALESCE(c.development_project_id::text, ''),
		'', project.name, COALESCE(project.remote_url, ''), '', project.project_kind,
		p.name, COALESCE(p.model, ''), COALESCE(p.reasoning_effort, ''), COALESCE(p.service_tier, ''),
		p.sandbox, p.approval_policy, p.network_enabled, m.body, m.discord_user_id,
		m.display_name, m.username, COALESCE(m.github_user_id, 0), COALESCE(m.github_login, ''),
		COALESCE(m.github_binding_id::text, ''), COALESCE(m.binding_version, 0), m.access_snapshot
		FROM discord_conversations c JOIN discord_input_messages m ON m.conversation_id = c.id
		JOIN discord_forums f ON f.id = c.forum_id AND f.forum_type = 'development'
		JOIN discord_development_environments environment ON environment.id=f.development_environment_id
		JOIN development_projects project ON project.id=c.development_project_id
		JOIN agent_profiles p ON p.id = c.agent_profile_id
		WHERE c.id = $1 AND m.message_id = $2
			AND f.binding_status='active'
			AND project.availability_status='available'
			AND environment.status='running'`, job.DiscordConversationID, job.DiscordMessageID).
		Scan(&result.ConversationID, &result.GuildID, &result.ThreadID, &result.MessageID, &result.OwnerUserID,
			&result.ForumID, &result.EnvironmentID,
			&projectID, &result.Owner, &result.Repository, &result.CloneURL, &result.DefaultBranch,
			&result.ProjectKind,
			&result.ProfileName, &result.Model, &result.ReasoningEffort,
			&result.ServiceTier, &result.Sandbox, &result.ApprovalPolicy, &result.NetworkEnabled,
			&result.Body, &result.DiscordUserID, &result.DisplayName, &result.Username,
			&result.GitHubUserID, &result.GitHubLogin, &result.BindingID, &result.BindingVersion, &result.Access)
	if err != nil {
		return discordJobContext{}, err
	}
	if job.Instruction != "" {
		result.Body = job.Instruction
	}
	if projectID.String != "" {
		result.ProjectID, err = uuid.Parse(projectID.String)
	}
	result.HasRemote = strings.TrimSpace(result.CloneURL) != ""
	return result, err
}

func (p *Processor) handleDiscordTool(ctx context.Context, claimed *codexcontrol.ClaimedControl,
	runtime devcontainer.Runtime, workspace ports.Workspace, request codex.ToolCallRequest,
) (codex.ToolCallResult, error) {
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
	if runtime.ProjectKind != "git" {
		return codex.ToolCallResult{}, errors.New("当前项目不是 Git 仓库")
	}
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
			_, err = p.db.ExecContext(ctx, `UPDATE development_projects project
				SET head_sha=$2, dirty=false, updated_at=now()
				FROM discord_forums forum
				WHERE forum.id=$1 AND forum.development_project_id=project.id`,
				runtime.ForumID, sha)
		}
		return codex.TextToolResult(fmt.Sprintf(`{"sha":%q}`, sha), err == nil), err
	case "publish_branch":
		if runtime.RemoteURL == "" {
			return codex.ToolCallResult{}, errors.New("当前项目没有远端，不能发布分支")
		}
		branch, sha, err := p.development.Publish(ctx, runtime)
		if err == nil {
			_, err = p.db.ExecContext(ctx, `UPDATE development_projects project
				SET head_sha=$2, dirty=false, branch=$3, updated_at=now()
				FROM discord_forums forum
				WHERE forum.id=$1 AND forum.development_project_id=project.id`,
				runtime.ForumID, sha, branch)
		}
		return codex.TextToolResult(fmt.Sprintf(`{"branch":%q,"sha":%q}`, branch, sha), err == nil), err
	default:
		return codex.ToolCallResult{}, fmt.Errorf("本地 Git 工具 %s 未授权", request.Tool)
	}
}

func (p *Processor) refreshDiscordWorkspaceState(ctx context.Context, runtime devcontainer.Runtime) {
	if runtime.ProjectKind != "git" {
		return
	}
	status, statusErr := p.development.Git(ctx, runtime, "status", "--porcelain=v1")
	head, headErr := p.development.Git(ctx, runtime, "rev-parse", "HEAD")
	if statusErr != nil || headErr != nil {
		cause := statusErr
		if cause == nil {
			cause = headErr
		}
		p.logger.Warn("刷新开发项目 Git 状态失败", zap.Error(cause),
			zap.String("forum_id", runtime.ForumID.String()))
		return
	}
	_, _ = p.db.ExecContext(ctx, `UPDATE development_projects project
		SET head_sha=$2, dirty=$3, updated_at=now()
		FROM discord_forums forum
		WHERE forum.id=$1 AND forum.development_project_id=project.id`,
		runtime.ForumID, strings.TrimSpace(head), strings.TrimSpace(status) != "")
}
