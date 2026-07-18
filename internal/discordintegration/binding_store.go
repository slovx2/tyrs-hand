package discordintegration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type SQLBindingStore struct{ db *sql.DB }

func NewSQLBindingStore(db *sql.DB) *SQLBindingStore { return &SQLBindingStore{db: db} }

func (s *SQLBindingStore) SaveOAuthState(ctx context.Context, stateHash string, state OAuthState, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO discord_oauth_states
		(state_hash, guild_id, discord_user_id, code_verifier_ciphertext, code_verifier_nonce, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)`, stateHash, state.GuildID, state.DiscordUserID,
		state.VerifierCiphertext, state.VerifierNonce, expiresAt)
	return err
}

func (s *SQLBindingStore) ConsumeOAuthState(ctx context.Context, stateHash string, now time.Time) (OAuthState, error) {
	var result OAuthState
	err := s.db.QueryRowContext(ctx, `UPDATE discord_oauth_states SET consumed_at = $2
		WHERE state_hash = $1 AND consumed_at IS NULL AND expires_at > $2
		RETURNING guild_id, discord_user_id, code_verifier_nonce, code_verifier_ciphertext`, stateHash, now).
		Scan(&result.GuildID, &result.DiscordUserID, &result.VerifierNonce, &result.VerifierCiphertext)
	return result, err
}

func (s *SQLBindingStore) Bind(ctx context.Context, input Binding) (Binding, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Binding{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var current Binding
	err = tx.QueryRowContext(ctx, `SELECT guild_id, discord_user_id, github_user_id, github_login, version
		FROM discord_identity_bindings WHERE guild_id = $1 AND discord_user_id = $2 AND status = 'active' FOR UPDATE`,
		input.GuildID, input.DiscordUserID).
		Scan(&current.GuildID, &current.DiscordUserID, &current.GitHubUserID, &current.GitHubLogin, &current.Version)
	if err == nil {
		if current.GitHubUserID != input.GitHubUserID {
			return Binding{}, errors.New("当前 Discord 用户已经绑定 GitHub，请先解绑")
		}
		_, err = tx.ExecContext(ctx, `UPDATE discord_identity_bindings SET github_login = $3
			WHERE guild_id = $1 AND discord_user_id = $2 AND status = 'active'`, input.GuildID, input.DiscordUserID, input.GitHubLogin)
		current.GitHubLogin = input.GitHubLogin
		return current, commitWithValue(tx, current, err)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Binding{}, err
	}
	var otherDiscordID string
	err = tx.QueryRowContext(ctx, `SELECT discord_user_id FROM discord_identity_bindings
		WHERE guild_id = $1 AND github_user_id = $2 AND status = 'active' FOR UPDATE`,
		input.GuildID, input.GitHubUserID).Scan(&otherDiscordID)
	if err == nil {
		return Binding{}, errors.New("这个 GitHub 用户已经绑定到当前 Server 的其他 Discord 用户")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Binding{}, err
	}
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(max(version), 0) + 1 FROM discord_identity_bindings
		WHERE guild_id = $1 AND discord_user_id = $2`, input.GuildID, input.DiscordUserID).Scan(&input.Version)
	if err != nil {
		return Binding{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO discord_identity_bindings
		(guild_id, discord_user_id, github_user_id, github_login, version)
		VALUES ($1, $2, $3, $4, $5)`, input.GuildID, input.DiscordUserID,
		input.GitHubUserID, input.GitHubLogin, input.Version)
	return input, commitWithValue(tx, input, err)
}

func (s *SQLBindingStore) Unbind(ctx context.Context, guildID, discordUserID string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE discord_identity_bindings SET status = 'unbound', unbound_at = now()
		WHERE guild_id = $1 AND discord_user_id = $2 AND status = 'active'`, guildID, discordUserID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("当前 Discord 用户没有有效的 GitHub 绑定")
	}
	return nil
}

func (s *SQLBindingStore) CurrentBinding(ctx context.Context, guildID, discordUserID string) (Binding, error) {
	var result Binding
	err := s.db.QueryRowContext(ctx, `SELECT guild_id, discord_user_id, github_user_id, github_login, version
		FROM discord_identity_bindings WHERE guild_id = $1 AND discord_user_id = $2 AND status = 'active'`,
		guildID, discordUserID).
		Scan(&result.GuildID, &result.DiscordUserID, &result.GitHubUserID, &result.GitHubLogin, &result.Version)
	return result, err
}

func commitWithValue[T any](tx *sql.Tx, value T, previous error) error {
	if previous != nil {
		return previous
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交身份绑定: %w", err)
	}
	return nil
}

var _ BindingStore = (*SQLBindingStore)(nil)
