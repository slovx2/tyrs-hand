package settings

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/slovx2/tyrs-hand/internal/secrets"
	"github.com/slovx2/tyrs-hand/internal/security"
)

const (
	agentProviderKey = "agent.provider"
	providerAPIKey   = "agent.provider.api_key"
)

type AgentProvider struct {
	ProviderType    string `json:"providerType"`
	BaseURL         string `json:"baseUrl,omitempty"`
	Model           string `json:"model,omitempty"`
	Reasoning       string `json:"reasoningEffort,omitempty"`
	ServiceTier     string `json:"serviceTier,omitempty"`
	ProxyURL        string `json:"proxyUrl,omitempty"`
	Configured      bool   `json:"configured"`
	ConfigSignature string `json:"configSignature"`
}

type AgentProviderInput struct {
	ProviderType string `json:"providerType"`
	BaseURL      string `json:"baseUrl"`
	APIKey       string `json:"apiKey"`
	Model        string `json:"model"`
	Reasoning    string `json:"reasoningEffort"`
	ServiceTier  string `json:"serviceTier"`
	ProxyURL     string `json:"proxyUrl"`
}

type providerRecord struct {
	AgentProvider
	CredentialVersion string `json:"credentialVersion,omitempty"`
}

type Service struct {
	db      *sql.DB
	secrets *secrets.Store
}

func NewService(db *sql.DB, secretStore *secrets.Store) *Service {
	return &Service{db: db, secrets: secretStore}
}

func (s *Service) AgentProvider(ctx context.Context) (AgentProvider, error) {
	record, err := s.record(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentProvider{ProviderType: "device-code", Configured: false}, nil
	}
	return record.AgentProvider, err
}

func (s *Service) SaveAgentProvider(ctx context.Context, input AgentProviderInput) error {
	input.ProviderType = strings.TrimSpace(input.ProviderType)
	if input.ProviderType != "device-code" && input.ProviderType != "api-key" {
		return errors.New("配置的 Provider 认证方式必须是 device-code 或 api-key")
	}
	if err := validateURL(input.BaseURL, "Base URL"); err != nil {
		return err
	}
	if err := validateURL(input.ProxyURL, "代理 URL"); err != nil {
		return err
	}
	old, err := s.record(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	credentialVersion := old.CredentialVersion
	if input.APIKey != "" {
		credentialVersion = security.Digest(input.APIKey)
	}
	if input.ProviderType == "api-key" && credentialVersion == "" {
		return errors.New("选择 API Key 认证方式时必须配置 API Key")
	}
	record := providerRecord{AgentProvider: AgentProvider{
		ProviderType: input.ProviderType, BaseURL: strings.TrimRight(input.BaseURL, "/"),
		Model: input.Model, Reasoning: input.Reasoning, ServiceTier: input.ServiceTier,
		ProxyURL: input.ProxyURL, Configured: input.ProviderType == "api-key" || old.Configured,
	}, CredentialVersion: credentialVersion}
	connectionChanged := providerConnectionChanged(old, record)
	if old.ConfigSignature != "" && !connectionChanged {
		record.ConfigSignature = old.ConfigSignature
	} else {
		record.ConfigSignature = signature(record)
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if input.APIKey != "" {
		if err := s.secrets.PutTx(ctx, tx, providerAPIKey, []byte(input.APIKey)); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO platform_settings(setting_key, value) VALUES ($1, $2)
		ON CONFLICT(setting_key) DO UPDATE SET value = EXCLUDED.value,
			version = platform_settings.version + 1, updated_at = now()`, agentProviderKey, data)
	if err != nil {
		return err
	}
	if old.ConfigSignature != "" && connectionChanged {
		if _, err := tx.ExecContext(ctx, "UPDATE work_items SET context_version = context_version + 1, updated_at = now() WHERE state = 'open'"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE discord_conversations SET context_version = context_version + 1, updated_at = now()"); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Service) APIKey(ctx context.Context) ([]byte, error) {
	return s.secrets.Get(ctx, providerAPIKey)
}

func (s *Service) PrepareCodexHome(ctx context.Context, codexHome, sharedHome string) (AgentProvider, []string, error) {
	provider, err := s.AgentProvider(ctx)
	if err != nil {
		return AgentProvider{}, nil, err
	}
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		return AgentProvider{}, nil, err
	}
	if provider.ProviderType == "api-key" {
		apiKey, err := s.APIKey(ctx)
		if err != nil {
			return AgentProvider{}, nil, err
		}
		auth, err := json.Marshal(map[string]any{"auth_mode": "apikey", "OPENAI_API_KEY": string(apiKey)})
		if err != nil {
			return AgentProvider{}, nil, err
		}
		if err := writeSecretFile(filepath.Join(codexHome, "auth.json"), auth); err != nil {
			return AgentProvider{}, nil, err
		}
	} else {
		sharedAuth, err := os.ReadFile(filepath.Join(sharedHome, "auth.json"))
		if err != nil {
			return AgentProvider{}, nil, errors.New("共享 Codex 账号尚未完成 Device Code 登录")
		}
		if err := writeSecretFile(filepath.Join(codexHome, "auth.json"), sharedAuth); err != nil {
			return AgentProvider{}, nil, err
		}
	}
	if provider.BaseURL != "" {
		if err := writeProviderConfig(filepath.Join(codexHome, "config.toml"), provider.BaseURL); err != nil {
			return AgentProvider{}, nil, err
		}
	}
	environment := make([]string, 0, 4)
	if provider.BaseURL != "" {
		environment = append(environment, "OPENAI_BASE_URL="+provider.BaseURL)
	}
	if provider.ProxyURL != "" {
		environment = append(environment, "HTTP_PROXY="+provider.ProxyURL, "HTTPS_PROXY="+provider.ProxyURL)
	}
	return provider, environment, nil
}

func (s *Service) record(ctx context.Context) (providerRecord, error) {
	var raw []byte
	if err := s.db.QueryRowContext(ctx, "SELECT value FROM platform_settings WHERE setting_key = $1", agentProviderKey).Scan(&raw); err != nil {
		return providerRecord{}, err
	}
	var result providerRecord
	return result, json.Unmarshal(raw, &result)
}

func signature(value providerRecord) string {
	value.ConfigSignature = ""
	data, _ := json.Marshal(value)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func providerConnectionChanged(old, current providerRecord) bool {
	return old.ProviderType != current.ProviderType || old.BaseURL != current.BaseURL ||
		old.ProxyURL != current.ProxyURL || old.CredentialVersion != current.CredentialVersion
}

func validateURL(value, name string) error {
	if value == "" {
		return nil
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return errors.New(name + " 不是有效的绝对 URL")
	}
	return nil
}

func writeSecretFile(path string, data []byte) error {
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(temporary, 0o600); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func writeProviderConfig(path, baseURL string) error {
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	lines := strings.Split(string(existing), "\n")
	filtered := make([]string, 0, len(lines))
	inRoot := true
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			inRoot = false
		}
		if inRoot && strings.HasPrefix(trimmed, "openai_base_url") {
			continue
		}
		filtered = append(filtered, line)
	}
	content := "openai_base_url = " + strconv.Quote(baseURL) + "\n"
	remaining := strings.TrimLeft(strings.Join(filtered, "\n"), "\n")
	if remaining != "" {
		content += remaining
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
	}
	return writeSecretFile(path, []byte(content))
}
