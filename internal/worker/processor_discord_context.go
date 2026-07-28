package worker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/devcontainer"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/slovx2/tyrs-hand/internal/ports"
)

func (p *Processor) discordTurnInput(ctx context.Context, jobCtx discordJobContext,
	workspace string, skills []ports.SkillRef,
) (ports.TurnInput, error) {
	runtime, err := p.development.Runtime(ctx, jobCtx.EnvironmentID, jobCtx.ForumID, jobCtx.ConversationID)
	if err != nil {
		return ports.TurnInput{}, err
	}
	attachments, err := p.prepareDiscordAttachments(ctx, jobCtx.IntentID, jobCtx.MessageID, runtime)
	if err != nil {
		return ports.TurnInput{}, err
	}
	identity := discordintegration.MessageIdentity{
		DiscordUserID: jobCtx.DiscordUserID, GitHubUserID: jobCtx.GitHubUserID,
		GitHubLogin: jobCtx.GitHubLogin, BindingID: jobCtx.BindingID,
		BindingVersion: jobCtx.BindingVersion, Access: jobCtx.Access, MessageID: jobCtx.MessageID,
		DisplayName: jobCtx.DisplayName, Username: jobCtx.Username,
	}
	if err := p.db.QueryRowContext(ctx, "SELECT guild_id FROM discord_conversations WHERE id = $1",
		jobCtx.ConversationID).Scan(&identity.GuildID); err != nil {
		return ports.TurnInput{}, err
	}
	additional := discordintegration.AdditionalContext(identity)
	var images []ports.LocalImageInput
	var files []map[string]string
	for _, attachment := range attachments {
		absolute := filepath.ToSlash(filepath.Join(runtime.Workspace, filepath.FromSlash(attachment.RelativePath)))
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
		additional["discord_message_attachments"] = ports.AdditionalContextEntry{
			Kind: "application", Value: string(encoded),
		}
	}
	return ports.TurnInput{Text: jobCtx.Body, ClientUserMessageID: jobCtx.MessageID,
		LocalImages: images, AdditionalContext: additional, Skills: skills}, nil
}

func (p *Processor) prepareDiscordAttachments(ctx context.Context, intentID uuid.UUID,
	messageID string, runtime devcontainer.Runtime,
) ([]discordintegration.SavedAttachment, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT attachment.discord_attachment_id,
		attachment.source_url, attachment.original_filename,
		attachment.media_type, attachment.size_bytes FROM discord_attachments attachment
		JOIN discord_input_messages message ON message.message_id = attachment.message_id
		WHERE attachment.status = 'pending' AND
			(message.turn_intent_id = NULLIF($1::text, '')::uuid OR
			 ($1 = '' AND message.message_id = $2))
		ORDER BY message.received_at DESC, attachment.created_at DESC, attachment.id DESC
		LIMIT $3`, optionalUUIDString(intentID), messageID, discordintegration.DefaultMaxAttachments)
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
		temporary, temporaryErr := os.MkdirTemp("", "tyrs-hand-discord-attachments-*")
		if temporaryErr != nil {
			return nil, temporaryErr
		}
		defer func() { _ = os.RemoveAll(temporary) }()
		saved, downloadErr := discordintegration.NewAttachmentDownloader(nil).Download(ctx, temporary, pending)
		if downloadErr != nil {
			return nil, downloadErr
		}
		if err := p.development.CopyToRuntime(ctx, runtime, temporary, runtime.Workspace); err != nil {
			return nil, err
		}
		for _, attachment := range saved {
			_, err = p.db.ExecContext(ctx, `UPDATE discord_attachments SET kind = $4, media_type = $5,
				size_bytes = $6, sha256 = $7, relative_path = $8, status = 'ready'
				WHERE discord_attachment_id = $2 AND message_id IN (
					SELECT message_id FROM discord_input_messages WHERE
					turn_intent_id = NULLIF($1::text, '')::uuid OR ($1 = '' AND message_id = $3)
				)`, optionalUUIDString(intentID), attachment.ID,
				messageID, attachment.Kind, attachment.MediaType, attachment.Size,
				attachment.SHA256, attachment.RelativePath)
			if err != nil {
				return nil, err
			}
		}
	}
	rows, err = p.db.QueryContext(ctx, `SELECT attachment.discord_attachment_id, attachment.kind,
		attachment.original_filename, attachment.media_type, attachment.size_bytes,
		attachment.sha256, attachment.relative_path FROM discord_attachments attachment
		JOIN discord_input_messages message ON message.message_id = attachment.message_id
		WHERE attachment.status = 'ready' AND
			(message.turn_intent_id = NULLIF($1::text, '')::uuid OR
			 ($1 = '' AND message.message_id = $2))
		ORDER BY message.received_at DESC, attachment.created_at DESC, attachment.id DESC
		LIMIT $3`, optionalUUIDString(intentID), messageID, discordintegration.DefaultMaxAttachments)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []discordintegration.SavedAttachment
	for rows.Next() {
		var attachment discordintegration.SavedAttachment
		if err := rows.Scan(&attachment.ID, &attachment.Kind, &attachment.Filename,
			&attachment.MediaType, &attachment.Size, &attachment.SHA256, &attachment.RelativePath); err != nil {
			return nil, err
		}
		result = append(result, attachment)
	}
	return result, rows.Err()
}

func (p *Processor) addDiscordContributor(ctx context.Context, runID, conversationID,
	intentID uuid.UUID, turnID string,
) error {
	_, err := p.db.ExecContext(ctx, `INSERT INTO discord_turn_contributors
		(run_id, conversation_id, external_turn_id, discord_user_id, first_message_id,
		github_binding_id, github_user_id, github_login, binding_version)
		SELECT DISTINCT ON (discord_user_id) $1, $2, $3, discord_user_id, message_id,
		github_binding_id, github_user_id, github_login, binding_version
		FROM discord_input_messages WHERE turn_intent_id = $4
		ORDER BY discord_user_id, received_at, message_id
		ON CONFLICT(run_id, discord_user_id) DO NOTHING`, runID, conversationID, turnID, intentID)
	return err
}

func optionalUUIDString(value uuid.UUID) string {
	if value == uuid.Nil {
		return ""
	}
	return value.String()
}
