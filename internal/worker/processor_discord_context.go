package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/slovx2/tyrs-hand/internal/domain"
	"github.com/slovx2/tyrs-hand/internal/ports"
	"github.com/slovx2/tyrs-hand/internal/queue"
)

func (p *Processor) ensureDiscordThread(ctx context.Context, runtime *codex.Runtime, job domain.Job, options ports.ThreadOptions, codexHome, providerSignature string) (uuid.UUID, string, error) {
	var dbID uuid.UUID
	var threadID string
	err := p.db.QueryRowContext(ctx, `SELECT id, external_thread_id FROM agent_threads
		WHERE source_type = 'discord_conversation' AND discord_conversation_id = $1
			AND agent_profile_id = $2 AND context_version = (
				SELECT context_version FROM discord_conversations WHERE id = $1)
			AND status = 'active' AND codex_home_key = $3 AND provider_signature = $4
			AND last_used_at > now() - interval '30 days'`,
		job.DiscordConversationID, job.AgentProfileID, codexHome, providerSignature).Scan(&dbID, &threadID)
	if err == nil {
		if resumeErr := runtime.ResumeThread(ctx, threadID, options); resumeErr == nil {
			return dbID, threadID, nil
		}
		_, _ = p.db.ExecContext(ctx, "UPDATE agent_threads SET status = 'stale' WHERE id = $1", dbID)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, "", err
	}
	summary, err := p.discordHandoffSummary(ctx, job.DiscordConversationID)
	if err != nil {
		return uuid.Nil, "", err
	}
	if summary != "" {
		options.DeveloperInstructions += "\n\nPersistent handoff summary from the previous Discord conversation thread:\n" + summary
	}
	threadID, err = runtime.StartThread(ctx, options)
	if err != nil {
		return uuid.Nil, "", err
	}
	err = p.db.QueryRowContext(ctx, `INSERT INTO agent_threads
		(work_item_id, source_type, discord_conversation_id, agent_profile_id, provider,
		external_thread_id, context_version, codex_home_key, provider_signature)
		VALUES (NULL, 'discord_conversation', $1, $2, 'codex', $3,
			(SELECT context_version FROM discord_conversations WHERE id = $1), $4, $5)
		ON CONFLICT(discord_conversation_id, agent_profile_id, context_version)
			WHERE discord_conversation_id IS NOT NULL DO UPDATE
		SET external_thread_id = EXCLUDED.external_thread_id, codex_home_key = EXCLUDED.codex_home_key,
			provider_signature = EXCLUDED.provider_signature, status = 'active', last_used_at = now()
		RETURNING id`, job.DiscordConversationID, job.AgentProfileID, threadID, codexHome, providerSignature).Scan(&dbID)
	return dbID, threadID, err
}

func (p *Processor) discordHandoffSummary(ctx context.Context, conversationID uuid.UUID) (string, error) {
	var summary string
	err := p.db.QueryRowContext(ctx, `SELECT summary FROM discord_conversation_memories
		WHERE conversation_id = $1 ORDER BY version DESC LIMIT 1`, conversationID).Scan(&summary)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return summary, err
}

func (p *Processor) persistDiscordSummary(ctx context.Context, conversationID uuid.UUID, threadID, summary string) error {
	const maxSummaryBytes = 32 * 1024
	raw := []byte(summary)
	if len(raw) > maxSummaryBytes {
		raw = raw[:maxSummaryBytes]
	}
	_, err := p.db.ExecContext(ctx, `INSERT INTO discord_conversation_memories
		(conversation_id, summary, source_thread_id, version) VALUES ($1, $2, $3,
		COALESCE((SELECT max(version) + 1 FROM discord_conversation_memories WHERE conversation_id = $1), 1))`,
		conversationID, string(raw), threadID)
	return err
}

func (p *Processor) discordTurnInput(ctx context.Context, jobCtx discordJobContext, workspace string, skills []ports.SkillRef) (ports.TurnInput, error) {
	attachments, err := p.prepareDiscordAttachments(ctx, jobCtx.MessageID, workspace)
	if err != nil {
		return ports.TurnInput{}, err
	}
	identity := discordintegration.MessageIdentity{
		DiscordUserID: jobCtx.DiscordUserID, GitHubUserID: jobCtx.GitHubUserID,
		GitHubLogin: jobCtx.GitHubLogin, BindingID: jobCtx.BindingID, BindingVersion: jobCtx.BindingVersion,
		Access: jobCtx.Access, MessageID: jobCtx.MessageID, DisplayName: jobCtx.DisplayName, Username: jobCtx.Username,
	}
	if err := p.db.QueryRowContext(ctx, "SELECT guild_id FROM discord_conversations WHERE id = $1", jobCtx.ConversationID).Scan(&identity.GuildID); err != nil {
		return ports.TurnInput{}, err
	}
	additional := discordintegration.AdditionalContext(identity)
	var images []ports.LocalImageInput
	var files []map[string]string
	for _, attachment := range attachments {
		absolute := filepath.Join(workspace, filepath.FromSlash(attachment.RelativePath))
		if attachment.Kind == "image" {
			images = append(images, ports.LocalImageInput{Path: absolute, Detail: "auto"})
		} else {
			files = append(files, map[string]string{
				"filename": attachment.Filename, "relative_path": attachment.RelativePath,
				"media_type": attachment.MediaType, "sha256": attachment.SHA256,
			})
		}
	}
	if len(files) > 0 {
		encoded, _ := json.Marshal(map[string]any{"message_id": jobCtx.MessageID, "files": files})
		additional["discord_message_attachments"] = ports.AdditionalContextEntry{Kind: "application", Value: string(encoded)}
	}
	return ports.TurnInput{Text: jobCtx.Body, ClientUserMessageID: jobCtx.MessageID,
		LocalImages: images, AdditionalContext: additional, Skills: skills, OutputSchema: agentOutcomeSchema()}, nil
}

func (p *Processor) prepareDiscordAttachments(ctx context.Context, messageID, workspace string) ([]discordintegration.SavedAttachment, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT discord_attachment_id, source_url, original_filename, media_type, size_bytes
		FROM discord_attachments WHERE message_id = $1 AND status = 'pending' ORDER BY created_at, id`, messageID)
	if err != nil {
		return nil, err
	}
	var pending []discordintegration.AttachmentInput
	for rows.Next() {
		var input discordintegration.AttachmentInput
		if err := rows.Scan(&input.ID, &input.URL, &input.Filename, &input.MediaType, &input.Size); err != nil {
			_ = rows.Close()
			return nil, err
		}
		pending = append(pending, input)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(pending) > 0 {
		saved, downloadErr := discordintegration.NewAttachmentDownloader(nil).Download(ctx, workspace, pending)
		if downloadErr != nil {
			return nil, downloadErr
		}
		for _, attachment := range saved {
			_, err = p.db.ExecContext(ctx, `UPDATE discord_attachments SET kind = $3, media_type = $4,
				size_bytes = $5, sha256 = $6, relative_path = $7, status = 'ready'
				WHERE message_id = $1 AND discord_attachment_id = $2`, messageID, attachment.ID,
				attachment.Kind, attachment.MediaType, attachment.Size, attachment.SHA256, attachment.RelativePath)
			if err != nil {
				return nil, err
			}
		}
	}
	rows, err = p.db.QueryContext(ctx, `SELECT discord_attachment_id, kind, original_filename, media_type,
		size_bytes, sha256, relative_path FROM discord_attachments WHERE message_id = $1 AND status = 'ready' ORDER BY created_at, id`, messageID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []discordintegration.SavedAttachment
	for rows.Next() {
		var attachment discordintegration.SavedAttachment
		if err := rows.Scan(&attachment.ID, &attachment.Kind, &attachment.Filename, &attachment.MediaType,
			&attachment.Size, &attachment.SHA256, &attachment.RelativePath); err != nil {
			return nil, err
		}
		result = append(result, attachment)
	}
	return result, rows.Err()
}

func (p *Processor) addDiscordContributor(ctx context.Context, conversationID uuid.UUID, turnID, messageID string) error {
	_, err := p.db.ExecContext(ctx, `INSERT INTO discord_turn_contributors
		(conversation_id, external_turn_id, discord_user_id, first_message_id,
		github_binding_id, github_user_id, github_login, binding_version)
		SELECT $1, $2, discord_user_id, message_id, github_binding_id, github_user_id, github_login, binding_version
		FROM discord_input_messages WHERE message_id = $3
		ON CONFLICT(conversation_id, external_turn_id, discord_user_id) DO NOTHING`, conversationID, turnID, messageID)
	return err
}

type discordTurnSteerer interface {
	SteerTurn(context.Context, string, string, ports.TurnInput) error
}

func (p *Processor) contributeAndSteerDiscord(ctx context.Context, runtime discordTurnSteerer,
	conversationID uuid.UUID, threadID, turnID, messageID string, input ports.TurnInput,
) error {
	// 权限集合必须在发送 Steer 前扩张；Steer 失败也不能撤回该贡献者。
	if err := p.addDiscordContributor(ctx, conversationID, turnID, messageID); err != nil {
		return err
	}
	return runtime.SteerTurn(ctx, threadID, turnID, input)
}

func (p *Processor) steerQueuedDiscord(ctx context.Context, runtime *codex.Runtime, claimed *queue.ClaimedJob, threadID, turnID string) error {
	var jobID uuid.UUID
	var messageID string
	err := p.db.QueryRowContext(ctx, `SELECT id, discord_message_id FROM job_intents
		WHERE source_type = 'discord_conversation' AND discord_conversation_id = $1
			AND status = 'queued' AND available_at <= now() ORDER BY created_at LIMIT 1`,
		claimed.DiscordConversationID).Scan(&jobID, &messageID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	jobCtx, err := p.loadDiscordContext(ctx, domain.Job{DiscordConversationID: claimed.DiscordConversationID, DiscordMessageID: messageID})
	if err != nil {
		return err
	}
	var workspace string
	if jobCtx.HasRepository {
		err = p.db.QueryRowContext(ctx, `SELECT w.path FROM discord_conversations c
			JOIN discord_workspaces w ON w.id = c.workspace_id WHERE c.id = $1`, claimed.DiscordConversationID).Scan(&workspace)
	} else {
		workspace = filepath.Join(p.cfg.DiscordWorkspaceRoot, "blank", claimed.DiscordConversationID.String())
	}
	if err != nil {
		return err
	}
	skills, err := resolveSkills(workspace, claimed.Skills)
	if err != nil {
		return err
	}
	input, err := p.discordTurnInput(ctx, jobCtx, workspace, skills)
	if err != nil {
		return err
	}
	if err := p.contributeAndSteerDiscord(ctx, runtime, claimed.DiscordConversationID,
		threadID, turnID, messageID, input); err != nil {
		return err
	}
	_, err = p.db.ExecContext(ctx, `UPDATE job_intents SET status = 'canceled', last_error = $2, updated_at = now()
		WHERE id = $1 AND status = 'queued'`, jobID, "steered into active turn "+claimed.ID.String())
	if err == nil {
		_, err = p.db.ExecContext(ctx, `UPDATE discord_input_messages SET status = 'processed', processed_at = now()
			WHERE message_id = $1`, messageID)
	}
	return err
}
