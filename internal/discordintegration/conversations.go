package discordintegration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/codexsettings"
	"github.com/slovx2/tyrs-hand/internal/officialapp"
	"github.com/slovx2/tyrs-hand/internal/participantidentity"
)

type IncomingMessage struct {
	GuildID                string
	ForumID                string
	ThreadID               string
	MessageID              string
	DiscordUserID          string
	DisplayName            string
	Username               string
	Title                  string
	Body                   string
	Model                  string
	ReasoningEffort        string
	ServiceTier            string
	CollaborationMode      string
	MentionsBot            bool
	ConfigurationConfirmed bool
	RememberPreferences    bool
	Attachments            []IncomingAttachment
}

type IncomingAttachment struct {
	ID         string
	URL        string
	Filename   string
	MediaType  string
	Size       int64
	Kind       string
	SHA256     string
	StorageKey string
}

type ConversationService struct {
	db             *sql.DB
	redis          *redis.Client
	attachmentRoot string
}

func NewConversationService(db *sql.DB) *ConversationService { return &ConversationService{db: db} }

func (s *ConversationService) ConfigureAttachmentStore(root string) {
	s.attachmentRoot = root
}

func (s *ConversationService) PersistAttachments(ctx context.Context, input *IncomingMessage) error {
	if input == nil || len(input.Attachments) == 0 {
		return nil
	}
	if strings.TrimSpace(s.attachmentRoot) == "" {
		return nil
	}
	if err := os.MkdirAll(s.attachmentRoot, 0o700); err != nil {
		return err
	}
	items := make([]AttachmentInput, 0, len(input.Attachments))
	for _, item := range input.Attachments {
		items = append(items, AttachmentInput{ID: item.ID, URL: item.URL,
			Filename: item.Filename, MediaType: item.MediaType, Size: item.Size})
	}
	saved, err := NewAttachmentDownloader(nil).Download(ctx, s.attachmentRoot, items)
	if err != nil {
		return err
	}
	byID := make(map[string]SavedAttachment, len(saved))
	for _, item := range saved {
		byID[item.ID] = item
	}
	for index := range input.Attachments {
		item := byID[input.Attachments[index].ID]
		input.Attachments[index].Kind = item.Kind
		input.Attachments[index].MediaType = item.MediaType
		input.Attachments[index].Size = item.Size
		input.Attachments[index].SHA256 = item.SHA256
		input.Attachments[index].StorageKey = item.RelativePath
	}
	return nil
}

func (s *ConversationService) CleanupAttachments(ctx context.Context) error {
	if strings.TrimSpace(s.attachmentRoot) == "" {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT a.id, a.storage_key
		FROM discord_attachments a WHERE a.status = 'ready'
		AND a.stored_at < now() - interval '7 days'
		AND NOT EXISTS (SELECT 1 FROM discord_input_messages pending
			WHERE pending.message_id = a.message_id AND pending.status = 'received'
				AND pending.official_submission_id IS NULL)
		AND NOT EXISTS (SELECT 1 FROM discord_input_messages message
			JOIN official_turn_submissions submission
				ON submission.id = message.official_submission_id
			WHERE message.message_id = a.message_id AND submission.status IN
			('queued','submitting','ambiguous'))
		ORDER BY a.stored_at LIMIT 100`)
	if err != nil {
		return err
	}
	type expired struct {
		id  uuid.UUID
		key string
	}
	var items []expired
	for rows.Next() {
		var item expired
		if err := rows.Scan(&item.id, &item.key); err != nil {
			_ = rows.Close()
			return err
		}
		items = append(items, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	root, err := filepath.Abs(s.attachmentRoot)
	if err != nil {
		return err
	}
	for _, item := range items {
		target := filepath.Join(root, filepath.FromSlash(item.key))
		relative, relErr := filepath.Rel(root, target)
		if relErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("附件清理路径越过持久卷")
		}
		if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE discord_attachments SET status = 'deleted',
			storage_key = NULL WHERE id = $1 AND status = 'ready'`, item.id); err != nil {
			return err
		}
	}
	return nil
}

func (s *ConversationService) BeginPost(ctx context.Context, input IncomingMessage) (uuid.UUID, error) {
	if err := validateIncomingMessage(input); err != nil {
		return uuid.Nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return uuid.Nil, err
	}
	defer func() { _ = tx.Rollback() }()
	forumID, ownerID, repositoryID, projectID, err := s.workspaceForum(ctx, tx,
		input.GuildID, input.ForumID)
	if err != nil {
		return uuid.Nil, err
	}
	access, err := s.access(ctx, tx, forumID, ownerID, input.DiscordUserID)
	if err != nil {
		return uuid.Nil, err
	}
	_ = repositoryID
	preferences := codexsettings.EffectivePreferences{}
	var profileID uuid.UUID
	if err := tx.QueryRowContext(ctx, `SELECT id FROM agent_profiles
		ORDER BY created_at LIMIT 1`).Scan(&profileID); err != nil {
		return uuid.Nil, err
	}
	userPreferences, remembered, err := loadUserCodexPreferences(ctx, tx,
		input.GuildID, input.DiscordUserID)
	if err != nil {
		return uuid.Nil, err
	}
	if remembered {
		applyUserCodexPreferences(&preferences, userPreferences)
	}
	if input.RememberPreferences || input.Model != "" {
		preferences.Model = input.Model
	}
	if input.RememberPreferences || input.ReasoningEffort != "" {
		preferences.ReasoningEffort = input.ReasoningEffort
	}
	if input.RememberPreferences || input.ServiceTier != "" {
		preferences.ServiceTier = input.ServiceTier
	}
	mode := input.CollaborationMode
	if mode == "" {
		mode = userPreferences.CollaborationMode
		if mode == "" {
			mode = "default"
		}
	}
	triggerMode := userPreferences.TriggerMode
	if triggerMode == "" {
		triggerMode = "interactive"
	}
	status, configurationStatus := "active", "configured"
	if !input.ConfigurationConfirmed {
		status, configurationStatus = "awaiting_configuration", "awaiting"
	}
	var conversationID uuid.UUID
	err = tx.QueryRowContext(ctx, `INSERT INTO discord_conversations
			(guild_id, forum_id, thread_id, starter_message_id, owner_discord_user_id,
			 repository_id, workspace_project_id, agent_profile_id, title, status,
			 model, reasoning_effort, service_tier,
		 collaboration_mode, trigger_mode,
		 configuration_status, configuration_deadline, configured_by_discord_user_id,
		 title_rename_status)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6::text,'')::uuid, NULLIF($7::text,'')::uuid,
			$8, $9, $10, NULLIF($11,''), NULLIF($12,''), NULLIF($13,''), $14, $15,
			$16, NULL, $17,
			'pending')
		ON CONFLICT(guild_id, thread_id) DO UPDATE SET last_activity_at = now(), updated_at = now()
		RETURNING id`, input.GuildID, forumID, input.ThreadID, input.MessageID, ownerID,
		optionalUUID(repositoryID), optionalUUID(projectID), profileID, input.Title, status,
		preferences.Model, preferences.ReasoningEffort,
		preferences.ServiceTier, mode, triggerMode, configurationStatus,
		input.DiscordUserID).Scan(&conversationID)
	if err != nil {
		return uuid.Nil, err
	}
	inserted, err := s.insertMessage(ctx, tx, conversationID, access, input)
	if err != nil {
		return uuid.Nil, err
	}
	if !inserted {
		return conversationID, tx.Commit()
	}
	if input.RememberPreferences {
		if err := saveUserCodexPreferences(ctx, tx, input.GuildID, input.DiscordUserID,
			userCodexPreferences{Model: preferences.Model,
				ReasoningEffort: preferences.ReasoningEffort, ServiceTier: preferences.ServiceTier,
				CollaborationMode: mode, TriggerMode: triggerMode}); err != nil {
			return uuid.Nil, err
		}
	}
	if input.ConfigurationConfirmed {
		if err := s.enqueueMessage(ctx, tx, conversationID, input.MessageID); err != nil {
			return uuid.Nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return uuid.Nil, err
	}
	if input.ConfigurationConfirmed {
		s.notifyJobs(ctx)
	}
	return conversationID, nil
}

func (s *ConversationService) Reply(ctx context.Context, input IncomingMessage) error {
	if err := validateIncomingMessage(input); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var conversationID, forumID uuid.UUID
	var ownerID, status, lifecycleState, triggerMode string
	err = tx.QueryRowContext(ctx, `SELECT conversation.id, conversation.forum_id,
		conversation.owner_discord_user_id, conversation.status, conversation.lifecycle_state,
		conversation.trigger_mode
			FROM discord_conversations conversation
			JOIN discord_forums forum ON forum.id=conversation.forum_id
			JOIN workspace_projects project ON project.id=forum.workspace_project_id
			WHERE conversation.guild_id=$1 AND conversation.thread_id=$2
			AND forum.binding_status='active'
			AND project.availability_status='available' FOR UPDATE OF conversation`,
		input.GuildID, input.ThreadID).Scan(&conversationID, &forumID, &ownerID, &status,
		&lifecycleState, &triggerMode)
	if err != nil {
		return err
	}
	access, err := s.access(ctx, tx, forumID, ownerID, input.DiscordUserID)
	if err != nil {
		return err
	}
	if lifecycleState != "active" {
		return codexcontrol.ErrControlArchived
	}
	inserted, err := s.insertMessage(ctx, tx, conversationID, access, input)
	if err != nil || !inserted {
		return err
	}
	if input.DiscordUserID != ownerID {
		memberKey := "conversation-member:" + conversationID.String() + ":" + input.DiscordUserID
		if err := enqueueDiscordOutbox(ctx, tx, memberKey, "thread.member.add",
			"channels/"+input.ThreadID+"/thread-members/"+input.DiscordUserID,
			map[string]string{"channelId": input.ThreadID, "userId": input.DiscordUserID}, ""); err != nil {
			return err
		}
	}
	actionablePlan, err := conversationHasActionablePlanTx(ctx, tx, conversationID)
	if err != nil {
		return err
	}
	shouldEnqueue := status == "active" &&
		(triggerMode == "interactive" || input.MentionsBot || actionablePlan)
	if shouldEnqueue {
		if err := s.enqueuePendingMessages(ctx, tx, conversationID, input.MessageID); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE discord_conversations SET last_activity_at = now(), updated_at = now()
		WHERE id = $1`, conversationID)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if shouldEnqueue {
		s.notifyJobs(ctx)
	}
	return nil
}

type ConversationConfiguration struct {
	Model           string
	ReasoningEffort string
	ServiceTier     string
}

func (s *ConversationService) FinalizeConfiguration(ctx context.Context, conversationID uuid.UUID,
	userID string,
) error {
	_, err := s.finalizeConfiguration(ctx, conversationID, userID, nil)
	return err
}

func (s *ConversationService) FinalizeConfigurationRevision(ctx context.Context,
	conversationID uuid.UUID, userID string, expectedRevision int64,
) (bool, error) {
	return s.finalizeConfiguration(ctx, conversationID, userID, &expectedRevision)
}

func (s *ConversationService) finalizeConfiguration(ctx context.Context, conversationID uuid.UUID,
	userID string, expectedRevision *int64,
) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var model, effort, tier, mode, triggerMode, guildID, threadID string
	var settingsRevision int64
	var forumID uuid.UUID
	var owner, configuredBy, status string
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(model,''), COALESCE(reasoning_effort,''),
		COALESCE(service_tier,'standard'), collaboration_mode,
		trigger_mode, guild_id, thread_id, forum_id, owner_discord_user_id,
		COALESCE(configured_by_discord_user_id,''), configuration_status, settings_revision
		FROM discord_conversations WHERE id = $1 FOR UPDATE`, conversationID).
		Scan(&model, &effort, &tier, &mode, &triggerMode, &guildID, &threadID, &forumID, &owner,
			&configuredBy, &status, &settingsRevision)
	if err != nil {
		return false, err
	}
	if userID != "" && userID != configuredBy {
		return false, errors.New("只有 Post 创建者可以修改该会话配置")
	}
	if expectedRevision != nil && settingsRevision != *expectedRevision {
		return true, tx.Commit()
	}
	if status == "configured" {
		return false, errors.New("该会话已经启动")
	}
	_, err = tx.ExecContext(ctx, `UPDATE discord_conversations SET model = NULLIF($2,''),
		reasoning_effort = NULLIF($3,''), service_tier = $4, configuration_status = 'configured',
		collaboration_mode = $5, configuration_deadline = NULL, status = 'active',
		settings_revision = settings_revision + 1, updated_at = now() WHERE id = $1`,
		conversationID, model, effort, tier, mode)
	if err != nil {
		return false, err
	}
	if err := saveUserCodexPreferences(ctx, tx, guildID, configuredBy, userCodexPreferences{
		Model: model, ReasoningEffort: effort, ServiceTier: tier,
		CollaborationMode: mode, TriggerMode: triggerMode,
	}); err != nil {
		return false, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT message_id FROM discord_input_messages
		WHERE conversation_id = $1 AND status = 'received' ORDER BY received_at, message_id`, conversationID)
	if err != nil {
		return false, err
	}
	var messages []string
	for rows.Next() {
		var messageID string
		if err := rows.Scan(&messageID); err != nil {
			_ = rows.Close()
			return false, err
		}
		messages = append(messages, messageID)
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	for _, messageID := range messages {
		if err := s.enqueueMessage(ctx, tx, conversationID, messageID); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	s.notifyJobs(ctx)
	return false, nil
}

func optionalPreference(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func (s *ConversationService) notifyJobs(ctx context.Context) {
	if s.redis != nil {
		_ = s.redis.Publish(ctx, officialapp.WakeupChannel, "queued").Err()
	}
}

func (s *ConversationService) insertMessage(ctx context.Context, tx *sql.Tx, conversationID uuid.UUID, access string, input IncomingMessage) (bool, error) {
	var bindingID *uuid.UUID
	var githubID *int64
	var login *string
	var version *int64
	var id uuid.UUID
	var ghID int64
	var ghLogin string
	var bindingVersion int64
	err := tx.QueryRowContext(ctx, `SELECT id, github_user_id, github_login, version
		FROM discord_identity_bindings WHERE guild_id = $1 AND discord_user_id = $2 AND status = 'active'`,
		input.GuildID, input.DiscordUserID).Scan(&id, &ghID, &ghLogin, &bindingVersion)
	if err == nil {
		bindingID, githubID, login, version = &id, &ghID, &ghLogin, &bindingVersion
	} else if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	participantID := participantidentity.ID(input.GuildID, input.DiscordUserID)
	result, err := tx.ExecContext(ctx, `INSERT INTO discord_input_messages
		(message_id, conversation_id, discord_user_id, participant_id, display_name, username,
		github_binding_id, github_user_id, github_login, binding_version, access_snapshot, body)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT(message_id) DO NOTHING`, input.MessageID, conversationID, input.DiscordUserID,
		participantID, input.DisplayName, input.Username, bindingID, githubID, login, version, access, input.Body)
	if err != nil {
		return false, err
	}
	inserted, _ := result.RowsAffected()
	if inserted == 0 {
		return false, nil
	}
	for _, attachment := range input.Attachments {
		kind := attachment.Kind
		if kind == "" {
			kind = "file"
			if strings.HasPrefix(attachment.MediaType, "image/") {
				kind = "image"
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO discord_attachments
			(message_id, discord_attachment_id, kind, original_filename, media_type, size_bytes,
			 source_url, sha256, relative_path, storage_key, stored_at, status)
			VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8,''), NULLIF($9,''),
			 NULLIF($9,''), CASE WHEN $9 = '' THEN NULL ELSE now() END,
			 CASE WHEN $9 = '' THEN 'pending' ELSE 'ready' END)`, input.MessageID, attachment.ID,
			kind, attachment.Filename, attachment.MediaType, attachment.Size, attachment.URL,
			attachment.SHA256, attachment.StorageKey)
		if err != nil {
			return false, err
		}
	}
	return true, nil
}

func (s *ConversationService) enqueueMessage(ctx context.Context, tx *sql.Tx,
	conversationID uuid.UUID, messageID string,
) error {
	return s.enqueueMessageWithDisplay(ctx, tx, conversationID, messageID, "")
}

func (s *ConversationService) enqueueMessageWithDisplay(ctx context.Context, tx *sql.Tx,
	conversationID uuid.UUID, messageID, displayInstruction string,
) error {
	var workspaceID, participantID uuid.UUID
	var body, model, effort, tier, mode string
	var displayName string
	err := tx.QueryRowContext(ctx, `SELECT project.workspace_id,m.participant_id,
		COALESCE(NULLIF(m.display_name,''),m.username),m.body,
		COALESCE(c.model,''),COALESCE(c.reasoning_effort,''),
		COALESCE(c.service_tier,''),c.collaboration_mode
		FROM discord_conversations c
		JOIN discord_input_messages m ON m.conversation_id=c.id
		JOIN workspace_projects project ON project.id=c.workspace_project_id
		WHERE c.id=$1 AND m.message_id=$2`, conversationID, messageID).Scan(
		&workspaceID, &participantID, &displayName, &body, &model, &effort, &tier, &mode)
	if err != nil {
		return err
	}
	preferences := officialapp.Preferences{Model: model, CollaborationMode: mode}
	preferences.ReasoningEffort = optionalPreference(effort)
	preferences.ServiceTier = optionalPreference(tier)
	additionalContext := participantidentity.AdditionalContext(participantidentity.Participant{
		ID: participantID, DisplayName: displayName,
	})
	submissionID, inserted, err := officialapp.EnqueueTx(ctx, tx, officialapp.EnqueueRequest{
		WorkspaceID: workspaceID, ConversationID: conversationID,
		SourceType: "discord_message", SourceOrder: messageID,
		DiscordMessageID: messageID, ClientMessageID: "discord:" + messageID,
		Instruction: body, DisplayInstruction: displayInstruction,
		Input: []officialapp.UserInput{officialapp.TextInput(body)}, Preferences: preferences,
		AdditionalContext:     additionalContext,
		DeveloperInstructions: participantidentity.DeveloperInstructions,
	})
	if err != nil || !inserted {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE discord_input_messages
		SET official_submission_id=$2 WHERE message_id=$1
		AND official_submission_id IS NULL`, messageID, submissionID)
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO official_submission_attachments(
			submission_id,attachment_id,ordinal)
			SELECT $2,attachment.id,row_number() OVER(
				ORDER BY attachment.created_at,attachment.id)-1
			FROM discord_attachments attachment WHERE attachment.message_id=$1
			  AND attachment.status='ready' AND attachment.storage_key IS NOT NULL
			ORDER BY attachment.created_at,attachment.id LIMIT 10
			ON CONFLICT DO NOTHING`, messageID, submissionID)
	}
	return err
}

func (s *ConversationService) workspaceForum(ctx context.Context, tx *sql.Tx,
	guildID, discordID string,
) (uuid.UUID, string, uuid.UUID, uuid.UUID, error) {
	var forumID uuid.UUID
	var repositoryID, projectID sql.NullString
	var owner string
	err := tx.QueryRowContext(ctx, `SELECT f.id, f.owner_discord_user_id,
		f.repository_id::text, f.workspace_project_id::text FROM discord_forums f
		JOIN discord_resources r ON r.id = f.resource_id
		JOIN worker_workspaces e ON e.id = f.workspace_id
		JOIN workspace_projects project ON project.id=f.workspace_project_id
		WHERE f.guild_id = $1 AND r.discord_id = $2 AND f.forum_type = 'workspace'
		  AND f.binding_status='active'
		  AND project.availability_status='available'`, guildID, discordID).
		Scan(&forumID, &owner, &repositoryID, &projectID)
	if err != nil {
		return uuid.Nil, "", uuid.Nil, uuid.Nil, err
	}
	repository, err := parseOptionalUUIDString(repositoryID.String)
	if err != nil {
		return uuid.Nil, "", uuid.Nil, uuid.Nil, err
	}
	project, err := parseOptionalUUIDString(projectID.String)
	return forumID, owner, repository, project, err
}

func parseOptionalUUIDString(value string) (uuid.UUID, error) {
	if value == "" {
		return uuid.Nil, nil
	}
	return uuid.Parse(value)
}

func (s *ConversationService) access(ctx context.Context, tx *sql.Tx, forumID uuid.UUID, ownerID, userID string) (string, error) {
	if userID == ownerID {
		return AccessOwner, nil
	}
	var access string
	err := tx.QueryRowContext(ctx, `SELECT access_level FROM discord_forum_access
		WHERE forum_id = $1 AND discord_user_id = $2`, forumID, userID).Scan(&access)
	if errors.Is(err, sql.ErrNoRows) || access == AccessReadOnly {
		return "", ErrReadOnly
	}
	if err != nil {
		return "", err
	}
	if access != AccessOperator {
		return "", fmt.Errorf("未知 Forum 权限 %q", access)
	}
	return access, nil
}

func validateIncomingMessage(input IncomingMessage) error {
	if input.GuildID == "" || input.ThreadID == "" || input.MessageID == "" || input.DiscordUserID == "" {
		return errors.New("discord 消息缺少 Guild、Thread、Message 或 User ID")
	}
	if strings.TrimSpace(input.Body) == "" && len(input.Attachments) == 0 {
		return errors.New("discord 消息没有支持的文字或附件")
	}
	if len(input.Attachments) > DefaultMaxAttachments {
		return fmt.Errorf("discord 附件不能超过 %d 个", DefaultMaxAttachments)
	}
	if input.CollaborationMode != "" && input.CollaborationMode != "default" &&
		input.CollaborationMode != "plan" {
		return errors.New("discord collaboration mode 无效")
	}
	_, err := json.Marshal(input)
	return err
}
