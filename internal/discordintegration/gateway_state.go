package discordintegration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
)

type GatewaySession struct {
	GuildID   string
	SessionID string
	ResumeURL string
	Sequence  int
}

func (m *Manager) GatewaySession(ctx context.Context, guildID string) (*GatewaySession, error) {
	var result GatewaySession
	err := m.db.QueryRowContext(ctx, `SELECT guild_id, session_id, resume_gateway_url, sequence
		FROM discord_gateway_sessions WHERE guild_id = $1`, guildID).
		Scan(&result.GuildID, &result.SessionID, &result.ResumeURL, &result.Sequence)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &result, err
}

func (m *Manager) SaveGatewaySession(ctx context.Context, session GatewaySession) error {
	_, err := m.db.ExecContext(ctx, `INSERT INTO discord_gateway_sessions
		(guild_id, session_id, resume_gateway_url, sequence) VALUES ($1, $2, $3, $4)
		ON CONFLICT(guild_id) DO UPDATE SET session_id = EXCLUDED.session_id,
			resume_gateway_url = EXCLUDED.resume_gateway_url, sequence = EXCLUDED.sequence, updated_at = now()`,
		session.GuildID, session.SessionID, session.ResumeURL, session.Sequence)
	return err
}

func (m *Manager) RecordInboundEvent(ctx context.Context, eventID, guildID, eventType string, payload any) (bool, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return false, err
	}
	result, err := m.db.ExecContext(ctx, `INSERT INTO discord_inbound_events(event_id, guild_id, event_type, payload)
		VALUES ($1, $2, $3, $4) ON CONFLICT(event_id) DO NOTHING`, eventID, guildID, eventType, encoded)
	if err != nil {
		return false, err
	}
	inserted, err := result.RowsAffected()
	return inserted == 1, err
}

func (m *Manager) CompleteInboundEvent(ctx context.Context, eventID string, cause error) error {
	status := "processed"
	var message any
	if cause != nil {
		status, message = "failed", cause.Error()
	}
	_, err := m.db.ExecContext(ctx, `UPDATE discord_inbound_events SET status = $2,
		error = $3, processed_at = now() WHERE event_id = $1`, eventID, status, message)
	return err
}

type GatewayConnector interface {
	Open(context.Context, *GatewaySession) error
}

type GatewayRunner struct {
	manager   *Manager
	guildID   string
	connector GatewayConnector
}

func NewGatewayRunner(manager *Manager, guildID string, connector GatewayConnector) *GatewayRunner {
	return &GatewayRunner{manager: manager, guildID: guildID, connector: connector}
}

func (r *GatewayRunner) Run(ctx context.Context) error {
	session, err := r.manager.GatewaySession(ctx, r.guildID)
	if err != nil {
		return err
	}
	return r.connector.Open(ctx, session)
}

func (s *ConversationService) Stop(ctx context.Context, guildID, threadID, requesterID string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var conversationID, forumID uuid.UUID
	var ownerID string
	err = tx.QueryRowContext(ctx, `SELECT id, forum_id, owner_discord_user_id FROM discord_conversations
		WHERE guild_id = $1 AND thread_id = $2`, guildID, threadID).Scan(&conversationID, &forumID, &ownerID)
	if err != nil {
		return 0, err
	}
	if _, err := s.access(ctx, tx, forumID, ownerID, requesterID); err != nil {
		return 0, err
	}
	var profileID uuid.UUID
	var repositoryID sql.NullString
	var contextVersion int64
	if err := tx.QueryRowContext(ctx, `SELECT agent_profile_id, repository_id::text, context_version
		FROM discord_conversations WHERE id = $1`, conversationID).Scan(
		&profileID, &repositoryID, &contextVersion); err != nil {
		return 0, err
	}
	var repository uuid.UUID
	if repositoryID.String != "" {
		repository, err = uuid.Parse(repositoryID.String)
		if err != nil {
			return 0, err
		}
	}
	requestID := uuid.New()
	_, inserted, err := codexcontrol.NewRepository(s.db, 0).Enqueue(ctx, tx, codexcontrol.EnqueueRequest{
		SourceType: codexcontrol.SourceDiscord, DiscordConversationID: conversationID,
		RepositoryID: repository, AgentProfileID: profileID, ContextVersion: contextVersion,
		IdempotencyKey: "discord:stop:" + requestID.String(), Operation: "interrupt",
		Instruction: "stopped from Discord", ReplyPolicy: "silent", ActorLogin: requesterID,
	})
	if err != nil {
		return 0, err
	}
	updateResult, err := tx.ExecContext(ctx, `UPDATE codex_turn_intents SET status = 'canceled',
		last_error_code = 'user_interrupt', last_error_message = 'stopped from Discord',
		finished_at = now(), updated_at = now()
		WHERE discord_conversation_id = $1 AND operation = 'turn_input'
		  AND status IN ('queued','retry_wait')`, conversationID)
	if err != nil {
		return 0, err
	}
	count, err := updateResult.RowsAffected()
	if err != nil {
		return 0, err
	}
	if count == 0 && inserted {
		count = 1
	}
	_, err = tx.ExecContext(ctx, `UPDATE discord_input_messages m SET status = 'canceled', processed_at = now()
		WHERE m.conversation_id = $1 AND m.status = 'received' AND EXISTS (
			SELECT 1 FROM codex_turn_intents i WHERE i.discord_message_id = m.message_id
				AND i.status = 'canceled' AND i.last_error_code = 'user_interrupt'
		)`, conversationID)
	if err != nil {
		return 0, err
	}
	return count, tx.Commit()
}
