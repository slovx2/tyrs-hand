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
	ConfigurationConfirmed bool
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
		AND NOT EXISTS (SELECT 1 FROM codex_turn_intents i
			WHERE i.discord_message_id = a.message_id AND i.status IN
			('placement_pending','queued','dispatching','awaiting_confirmation','running',
			 'reconciling','retry_wait'))
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
	forumID, ownerID, repositoryID, err := s.developmentForum(ctx, tx, input.GuildID, input.ForumID)
	if err != nil {
		return uuid.Nil, err
	}
	access, err := s.access(ctx, tx, forumID, ownerID, input.DiscordUserID)
	if err != nil {
		return uuid.Nil, err
	}
	var profileID uuid.UUID
	if err := tx.QueryRowContext(ctx, `SELECT id FROM agent_profiles ORDER BY created_at LIMIT 1`).Scan(&profileID); err != nil {
		return uuid.Nil, err
	}
	preferences, err := codexsettings.NewService(s.db).Resolve(ctx, repositoryID, forumID, profileID)
	if err != nil {
		return uuid.Nil, err
	}
	if input.Model != "" {
		preferences.Model = input.Model
	}
	if input.ReasoningEffort != "" {
		preferences.ReasoningEffort = input.ReasoningEffort
	}
	if input.ServiceTier != "" {
		preferences.ServiceTier = input.ServiceTier
	}
	status, configurationStatus := "active", "configured"
	var deadline any
	if !input.ConfigurationConfirmed {
		status, configurationStatus = "awaiting_configuration", "awaiting"
		deadline = "20 seconds"
	}
	var conversationID uuid.UUID
	err = tx.QueryRowContext(ctx, `INSERT INTO discord_conversations
		(guild_id, forum_id, thread_id, starter_message_id, owner_discord_user_id,
		 repository_id, agent_profile_id, title, status, model, reasoning_effort, service_tier,
		 configuration_status, configuration_deadline, configured_by_discord_user_id,
		 title_rename_status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NULLIF($10,''), NULLIF($11,''), $12,
			$13, CASE WHEN $14::text IS NULL THEN NULL ELSE now() + $14::interval END, $15, 'pending')
		ON CONFLICT(guild_id, thread_id) DO UPDATE SET last_activity_at = now(), updated_at = now()
		RETURNING id`, input.GuildID, forumID, input.ThreadID, input.MessageID, ownerID,
		repositoryID, profileID, input.Title, status, preferences.Model, preferences.ReasoningEffort,
		preferences.ServiceTier, configurationStatus, deadline, input.DiscordUserID).Scan(&conversationID)
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
	var ownerID, status string
	err = tx.QueryRowContext(ctx, `SELECT id, forum_id, owner_discord_user_id, status
		FROM discord_conversations WHERE guild_id = $1 AND thread_id = $2 FOR UPDATE`,
		input.GuildID, input.ThreadID).Scan(&conversationID, &forumID, &ownerID, &status)
	if err != nil {
		return err
	}
	access, err := s.access(ctx, tx, forumID, ownerID, input.DiscordUserID)
	if err != nil {
		return err
	}
	inserted, err := s.insertMessage(ctx, tx, conversationID, access, input)
	if err != nil || !inserted {
		return err
	}
	if status == "active" {
		if err := s.enqueueMessage(ctx, tx, conversationID, input.MessageID); err != nil {
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
	if status == "active" {
		s.notifyJobs(ctx)
	}
	return nil
}

type ConversationConfiguration struct {
	Model           string
	ReasoningEffort string
	ServiceTier     string
}

func (s *ConversationService) BeginConfigurationEdit(ctx context.Context, conversationID uuid.UUID,
	userID string,
) error {
	result, err := s.db.ExecContext(ctx, `UPDATE discord_conversations c SET configuration_status = 'editing',
		configuration_deadline = now() + interval '2 minutes', updated_at = now()
		WHERE c.id = $1 AND c.configuration_status IN ('awaiting','editing') AND (
			c.configured_by_discord_user_id = $2 OR c.owner_discord_user_id = $2 OR EXISTS(
				SELECT 1 FROM discord_forum_access a WHERE a.forum_id = c.forum_id
				AND a.discord_user_id = $2 AND a.access_level = 'operator'))`, conversationID, userID)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("配置已生效，或当前用户没有修改权限")
	}
	return nil
}

func (s *ConversationService) FinalizeConfiguration(ctx context.Context, conversationID uuid.UUID,
	userID string, selected *ConversationConfiguration,
) error {
	return s.finalizeConfiguration(ctx, conversationID, userID, selected, false)
}

func (s *ConversationService) finalizeConfiguration(ctx context.Context, conversationID uuid.UUID,
	userID string, selected *ConversationConfiguration, requireDue bool,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var model, effort, tier string
	var forumID uuid.UUID
	var owner, configuredBy, status string
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(model,''), COALESCE(reasoning_effort,''), service_tier,
		forum_id, owner_discord_user_id, COALESCE(configured_by_discord_user_id,''), configuration_status
		FROM discord_conversations WHERE id = $1 AND (
			$2 = false OR (configuration_status IN ('awaiting','editing') AND configuration_deadline <= now())
		) FOR UPDATE`, conversationID, requireDue).
		Scan(&model, &effort, &tier, &forumID, &owner, &configuredBy, &status)
	if err != nil {
		return err
	}
	if status == "configured" {
		return errors.New("该会话已经启动")
	}
	if userID != "" && userID != configuredBy && userID != owner {
		var operator bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM discord_forum_access
			WHERE forum_id = $1 AND discord_user_id = $2 AND access_level = 'operator')`, forumID, userID).
			Scan(&operator); err != nil || !operator {
			return errors.New("当前用户没有修改该会话配置的权限")
		}
	}
	if selected != nil {
		value := codexsettings.Preferences{Model: optionalPreference(selected.Model),
			ReasoningEffort: optionalPreference(selected.ReasoningEffort), ServiceTier: &selected.ServiceTier}
		if err := codexsettings.ValidatePreferences(value); err != nil {
			return err
		}
		model, effort, tier = strings.TrimSpace(selected.Model), selected.ReasoningEffort, selected.ServiceTier
	}
	_, err = tx.ExecContext(ctx, `UPDATE discord_conversations SET model = NULLIF($2,''),
		reasoning_effort = NULLIF($3,''), service_tier = $4, configuration_status = 'configured',
		configuration_deadline = NULL, status = 'active', updated_at = now() WHERE id = $1`,
		conversationID, model, effort, tier)
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT message_id FROM discord_input_messages
		WHERE conversation_id = $1 AND status = 'received' ORDER BY received_at, message_id`, conversationID)
	if err != nil {
		return err
	}
	var messages []string
	for rows.Next() {
		var messageID string
		if err := rows.Scan(&messageID); err != nil {
			_ = rows.Close()
			return err
		}
		messages = append(messages, messageID)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, messageID := range messages {
		if err := s.enqueueMessage(ctx, tx, conversationID, messageID); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.notifyJobs(ctx)
	return nil
}

func optionalPreference(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func (s *ConversationService) StartDueConfiguration(ctx context.Context) (bool, error) {
	var conversationID uuid.UUID
	err := s.db.QueryRowContext(ctx, `SELECT id FROM discord_conversations
		WHERE configuration_status IN ('awaiting','editing') AND configuration_deadline <= now()
		ORDER BY configuration_deadline LIMIT 1`).Scan(&conversationID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	err = s.finalizeConfiguration(ctx, conversationID, "", nil, true)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil && strings.Contains(err.Error(), "已经启动") {
		return true, nil
	}
	return err == nil, err
}

func (s *ConversationService) notifyJobs(ctx context.Context) {
	if s.redis != nil {
		_ = s.redis.Publish(ctx, codexcontrol.WakeupChannel, "queued").Err()
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

func (s *ConversationService) enqueueMessage(ctx context.Context, tx *sql.Tx, conversationID uuid.UUID, messageID string) error {
	var repositoryID sql.NullString
	var profileID uuid.UUID
	var contextVersion int64
	var body, actor, permission, actorDisplayName string
	var actorParticipantID uuid.UUID
	var allowedJSON []byte
	err := tx.QueryRowContext(ctx, `SELECT c.repository_id::text, c.agent_profile_id, c.context_version,
		m.body, COALESCE(m.github_login, ''), m.access_snapshot, m.participant_id,
		m.display_name, p.allowed_tools
		FROM discord_conversations c JOIN discord_input_messages m ON m.conversation_id = c.id
		JOIN agent_profiles p ON p.id = c.agent_profile_id
		WHERE c.id = $1 AND m.message_id = $2`, conversationID, messageID).Scan(
		&repositoryID, &profileID, &contextVersion, &body, &actor, &permission,
		&actorParticipantID, &actorDisplayName, &allowedJSON)
	if err != nil {
		return err
	}
	var repository uuid.UUID
	if repositoryID.String != "" {
		repository, err = uuid.Parse(repositoryID.String)
		if err != nil {
			return err
		}
	}
	var allowed []string
	if err := json.Unmarshal(allowedJSON, &allowed); err != nil {
		return err
	}
	_, _, err = codexcontrol.NewRepository(s.db, 0).Enqueue(ctx, tx, codexcontrol.EnqueueRequest{
		SourceType: codexcontrol.SourceDiscord, DiscordConversationID: conversationID,
		DiscordMessageID: messageID, RepositoryID: repository, AgentProfileID: profileID,
		ContextVersion: contextVersion, IdempotencyKey: "discord:message:" + messageID,
		Instruction: body, AllowedTools: allowed, ActorLogin: actor, ActorPermission: permission,
		ActorParticipantID: actorParticipantID, ActorDisplayName: actorDisplayName,
		ReplyPolicy: "silent", Behavior: "steer_if_active",
	})
	return err
}

func (s *ConversationService) developmentForum(ctx context.Context, tx *sql.Tx,
	guildID, discordID string,
) (uuid.UUID, string, uuid.UUID, error) {
	var forumID uuid.UUID
	var repositoryID uuid.UUID
	var owner string
	err := tx.QueryRowContext(ctx, `SELECT f.id, f.owner_discord_user_id, f.repository_id FROM discord_forums f
		JOIN discord_resources r ON r.id = f.resource_id
		JOIN discord_development_environments e ON e.id = f.development_environment_id
		WHERE f.guild_id = $1 AND r.discord_id = $2 AND f.forum_type = 'development'
		  AND e.status <> 'deleting'`, guildID, discordID).Scan(&forumID, &owner, &repositoryID)
	return forumID, owner, repositoryID, err
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
	_, err := json.Marshal(input)
	return err
}
