package github

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/secrets"
)

const (
	privateKeySecret = "github.app.private_key"
	webhookSecretKey = "github.app.webhook_secret"
)

type AppSettings struct {
	AppID         int64  `json:"appId"`
	ClientID      string `json:"clientId"`
	AppSlug       string `json:"appSlug"`
	PrivateKey    string `json:"privateKey,omitempty"`
	WebhookSecret string `json:"webhookSecret,omitempty"`
}

type Manager struct {
	db      *sql.DB
	secrets *secrets.Store

	mu       sync.RWMutex
	settings *AppSettings
	app      *AppClient
	provider *Provider
}

func NewManager(db *sql.DB, secretStore *secrets.Store) *Manager {
	return &Manager{db: db, secrets: secretStore}
}

func (m *Manager) Load(ctx context.Context) error {
	var settings AppSettings
	err := m.db.QueryRowContext(ctx, `SELECT app_id, COALESCE(client_id, ''), app_slug FROM github_app_configs ORDER BY created_at LIMIT 1`).
		Scan(&settings.AppID, &settings.ClientID, &settings.AppSlug)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	privateKey, err := m.secrets.Get(ctx, privateKeySecret)
	if err != nil {
		return err
	}
	webhookSecret, err := m.secrets.Get(ctx, webhookSecretKey)
	if err != nil {
		return err
	}
	app, err := NewAppClient(settings.AppID, privateKey, "")
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.settings = &settings
	m.app = app
	m.provider = NewProvider(webhookSecret, app)
	m.mu.Unlock()
	return nil
}

func (m *Manager) Save(ctx context.Context, settings AppSettings) error {
	if settings.AppID <= 0 || settings.AppSlug == "" || settings.PrivateKey == "" || settings.WebhookSecret == "" {
		return errors.New("配置的 GitHub App ID、Slug、Private Key 和 Webhook Secret 不能为空")
	}
	if _, err := NewAppClient(settings.AppID, []byte(settings.PrivateKey), ""); err != nil {
		return err
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := m.secrets.PutTx(ctx, tx, privateKeySecret, []byte(settings.PrivateKey)); err != nil {
		return err
	}
	if err := m.secrets.PutTx(ctx, tx, webhookSecretKey, []byte(settings.WebhookSecret)); err != nil {
		return err
	}
	var privateID, webhookID uuid.UUID
	if err := tx.QueryRowContext(ctx, "SELECT id FROM encrypted_secrets WHERE secret_key = $1", privateKeySecret).Scan(&privateID); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, "SELECT id FROM encrypted_secrets WHERE secret_key = $1", webhookSecretKey).Scan(&webhookID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM github_app_configs"); err != nil {
		return fmt.Errorf("替换 GitHub App: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO github_app_configs(app_id, client_id, app_slug, private_key_secret_id, webhook_secret_id)
		VALUES ($1, NULLIF($2, ''), $3, $4, $5)`,
		settings.AppID, settings.ClientID, settings.AppSlug, privateID, webhookID)
	if err != nil {
		return fmt.Errorf("保存 GitHub App: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交 GitHub App: %w", err)
	}
	return m.Load(ctx)
}

func (m *Manager) Current() (*AppSettings, *AppClient, *Provider, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.settings == nil {
		return nil, nil, nil, false
	}
	copy := *m.settings
	return &copy, m.app, m.provider, true
}
