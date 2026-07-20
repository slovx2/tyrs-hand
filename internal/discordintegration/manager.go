package discordintegration

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"github.com/slovx2/tyrs-hand/internal/secrets"
)

const botTokenSecretKey = "discord.bot_token"

type Manager struct {
	db      *sql.DB
	secrets *secrets.Store
}

func NewManager(db *sql.DB, secretStore *secrets.Store) *Manager {
	return &Manager{db: db, secrets: secretStore}
}

func (m *Manager) Settings(ctx context.Context) (Settings, error) {
	var value Settings
	err := m.db.QueryRowContext(ctx, `
		SELECT guild_id, enabled, community_enabled, COALESCE(application_id, ''), COALESCE(bot_user_id, ''),
			EXISTS(SELECT 1 FROM encrypted_secrets WHERE secret_key = $1)
		FROM discord_guilds ORDER BY updated_at DESC LIMIT 1`, botTokenSecretKey).
		Scan(&value.GuildID, &value.Enabled, &value.Community, &value.ApplicationID, &value.BotUserID, &value.TokenConfigured)
	if errors.Is(err, sql.ErrNoRows) {
		return Settings{Community: true}, nil
	}
	return value, err
}

func (m *Manager) SaveSettings(ctx context.Context, input SettingsInput) error {
	input.GuildID = strings.TrimSpace(input.GuildID)
	input.ApplicationID = strings.TrimSpace(input.ApplicationID)
	input.BotUserID = strings.TrimSpace(input.BotUserID)
	if !validSnowflake(input.GuildID) {
		return errors.New("discord Server ID 必须是有效的 Snowflake")
	}
	if input.ApplicationID != "" && !validSnowflake(input.ApplicationID) {
		return errors.New("discord Application ID 必须是有效的 Snowflake")
	}
	if input.BotUserID != "" && !validSnowflake(input.BotUserID) {
		return errors.New("discord Bot User ID 必须是有效的 Snowflake")
	}
	current, err := m.Settings(ctx)
	if err != nil {
		return err
	}
	if current.GuildID != "" && current.GuildID != input.GuildID {
		var resources int
		if err := m.db.QueryRowContext(ctx, "SELECT count(*) FROM discord_resources WHERE guild_id = $1", current.GuildID).Scan(&resources); err != nil {
			return err
		}
		if resources > 0 {
			return errors.New("已有受管 Discord 资源，不能直接切换 Server")
		}
	}
	if input.Enabled && input.BotToken == "" && !current.TokenConfigured {
		return errors.New("启用 Discord 前必须配置 Bot Token")
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if input.BotToken != "" {
		if err := m.secrets.PutTx(ctx, tx, botTokenSecretKey, []byte(input.BotToken)); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE discord_guilds SET enabled = false, updated_at = now() WHERE guild_id <> $1", input.GuildID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO discord_guilds(guild_id, enabled, community_enabled, application_id, bot_user_id)
		VALUES ($1, $2, true, NULLIF($3, ''), NULLIF($4, ''))
		ON CONFLICT(guild_id) DO UPDATE SET enabled = EXCLUDED.enabled,
			community_enabled = true, application_id = EXCLUDED.application_id,
			bot_user_id = EXCLUDED.bot_user_id, updated_at = now()`,
		input.GuildID, input.Enabled, input.ApplicationID, input.BotUserID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (m *Manager) BotToken(ctx context.Context) (string, error) {
	value, err := m.secrets.Get(ctx, botTokenSecretKey)
	return string(value), err
}

func (m *Manager) Status(ctx context.Context) (Status, error) {
	settings, err := m.Settings(ctx)
	if err != nil {
		return Status{}, err
	}
	result := Status{Configured: settings.GuildID != "" && settings.TokenConfigured, Enabled: settings.Enabled}
	if settings.GuildID == "" {
		return result, nil
	}
	err = m.db.QueryRowContext(ctx, `
		SELECT last_gateway_status, COALESCE(last_gateway_error, ''), last_gateway_at,
			(SELECT count(*) FROM integration_outbox WHERE integration = 'discord' AND status IN ('pending', 'retrying')),
			(SELECT count(*) FROM integration_outbox WHERE integration = 'discord' AND status = 'failed'),
			(SELECT count(*) FROM discord_initialization_operations WHERE status IN ('pending', 'running'))
		FROM discord_guilds WHERE guild_id = $1`, settings.GuildID).
		Scan(&result.GatewayStatus, &result.GatewayError, &result.LastGatewayAt,
			&result.PendingOutbox, &result.FailedOutbox, &result.PendingOperation)
	return result, err
}

func (m *Manager) SetGatewayStatus(ctx context.Context, guildID, status string, gatewayErr error) error {
	var message any
	if gatewayErr != nil {
		message = gatewayErr.Error()
	}
	_, err := m.db.ExecContext(ctx, `UPDATE discord_guilds SET last_gateway_status = $2,
		last_gateway_error = $3, last_gateway_at = now(), updated_at = now() WHERE guild_id = $1`, guildID, status, message)
	return err
}

func (m *Manager) Members(ctx context.Context) ([]Member, error) {
	settings, err := m.Settings(ctx)
	if err != nil {
		return nil, err
	}
	if settings.GuildID == "" {
		return []Member{}, nil
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT m.guild_id, m.discord_user_id, m.username, m.display_name,
			b.id IS NOT NULL, COALESCE(b.github_login, '')
		FROM discord_members m
		LEFT JOIN discord_identity_bindings b ON b.guild_id = m.guild_id
			AND b.discord_user_id = m.discord_user_id AND b.status = 'active'
		WHERE m.active = true AND m.guild_id = $1
		ORDER BY lower(m.display_name), m.discord_user_id`, settings.GuildID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []Member
	for rows.Next() {
		var member Member
		if err := rows.Scan(&member.GuildID, &member.DiscordUserID, &member.Username, &member.DisplayName,
			&member.Bound, &member.GitHubLogin); err != nil {
			return nil, err
		}
		result = append(result, member)
	}
	return result, rows.Err()
}

func validSnowflake(value string) bool {
	parsed, err := strconv.ParseUint(value, 10, 64)
	return err == nil && parsed > 0
}
