package discordintegration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
)

type IncomingMessage struct {
	GuildID       string
	ForumID       string
	ThreadID      string
	MessageID     string
	DiscordUserID string
	DisplayName   string
	Username      string
	Title         string
	Body          string
	Attachments   []IncomingAttachment
}

type IncomingAttachment struct {
	ID        string
	URL       string
	Filename  string
	MediaType string
	Size      int64
}

type ConversationService struct {
	db    *sql.DB
	redis *redis.Client
}

func NewConversationService(db *sql.DB) *ConversationService { return &ConversationService{db: db} }

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
	var conversationID uuid.UUID
	err = tx.QueryRowContext(ctx, `INSERT INTO discord_conversations
		(guild_id, forum_id, thread_id, starter_message_id, owner_discord_user_id,
		 repository_id, agent_profile_id, title, status)
		VALUES ($1, $2, $3, $4, $5, $6,
			(SELECT id FROM agent_profiles ORDER BY created_at LIMIT 1), $7, 'active')
		ON CONFLICT(guild_id, thread_id) DO UPDATE SET last_activity_at = now(), updated_at = now()
		RETURNING id`, input.GuildID, forumID, input.ThreadID, input.MessageID, ownerID,
		repositoryID, input.Title).Scan(&conversationID)
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
	if err := s.enqueueMessage(ctx, tx, conversationID, input.MessageID); err != nil {
		return uuid.Nil, err
	}
	if err := tx.Commit(); err != nil {
		return uuid.Nil, err
	}
	s.notifyJobs(ctx)
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
	participantID := uuid.NewSHA1(uuid.NameSpaceURL, []byte("discord://"+input.GuildID+"/users/"+input.DiscordUserID))
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
		kind := "file"
		if strings.HasPrefix(attachment.MediaType, "image/") {
			kind = "image"
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO discord_attachments
			(message_id, discord_attachment_id, kind, original_filename, media_type, size_bytes, source_url)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, input.MessageID, attachment.ID, kind,
			attachment.Filename, attachment.MediaType, attachment.Size, attachment.URL)
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
	var body, actor, permission string
	var allowedJSON []byte
	err := tx.QueryRowContext(ctx, `SELECT c.repository_id::text, c.agent_profile_id, c.context_version,
		m.body, COALESCE(m.github_login, ''), m.access_snapshot, p.allowed_tools
		FROM discord_conversations c JOIN discord_input_messages m ON m.conversation_id = c.id
		JOIN agent_profiles p ON p.id = c.agent_profile_id
		WHERE c.id = $1 AND m.message_id = $2`, conversationID, messageID).Scan(
		&repositoryID, &profileID, &contextVersion, &body, &actor, &permission, &allowedJSON)
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
