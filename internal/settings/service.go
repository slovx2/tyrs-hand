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
	agentProviderKey    = "agent.provider"
	providerAPIKey      = "agent.provider.api_key"
	globalAgentsKey     = "codex.global_agents"
	maxGlobalAgents     = 256 * 1024
	ModelSourceChatGPT  = "chatgpt"
	ModelSourceProvider = "provider"
)

type GlobalAgents struct {
	Content string `json:"content"`
}

type AgentProvider struct {
	ModelSource         string `json:"modelSource"`
	BaseURL             string `json:"baseUrl,omitempty"`
	Model               string `json:"model,omitempty"`
	Reasoning           string `json:"reasoningEffort,omitempty"`
	ServiceTier         string `json:"serviceTier,omitempty"`
	ProxyURL            string `json:"proxyUrl,omitempty"`
	ProviderConfigured  bool   `json:"providerConfigured"`
	ChatGPTConfigured   bool   `json:"chatgptConfigured"`
	ChatGPTAuthRevision int64  `json:"chatgptAuthRevision"`
	ConfigSignature     string `json:"configSignature"`
}

type AgentProviderInput struct {
	ModelSource string `json:"modelSource"`
	BaseURL     string `json:"baseUrl"`
	APIKey      string `json:"apiKey"`
	Model       string `json:"model"`
	Reasoning   string `json:"reasoningEffort"`
	ServiceTier string `json:"serviceTier"`
	ProxyURL    string `json:"proxyUrl"`
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
		record.AgentProvider = AgentProvider{ModelSource: ModelSourceProvider}
	} else if err != nil {
		return AgentProvider{}, err
	}
	agents, configured, err := s.globalAgents(ctx)
	if err != nil {
		return AgentProvider{}, err
	}
	if configured {
		digest := sha256.Sum256([]byte(agents.Content))
		combined := sha256.Sum256([]byte(record.ConfigSignature + ":" + hex.EncodeToString(digest[:])))
		record.ConfigSignature = hex.EncodeToString(combined[:])
	}
	return record.AgentProvider, nil
}

func (s *Service) GlobalAgents(ctx context.Context) (GlobalAgents, error) {
	value, _, err := s.globalAgents(ctx)
	return value, err
}

func (s *Service) SaveGlobalAgents(ctx context.Context, input GlobalAgents) error {
	input.Content = strings.ReplaceAll(input.Content, "\r\n", "\n")
	if len(input.Content) > maxGlobalAgents {
		return errors.New("全局 AGENTS.md 不能超过 256 KiB")
	}
	data, err := json.Marshal(input)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `INSERT INTO platform_settings(setting_key, value)
		VALUES ($1,$2) ON CONFLICT(setting_key) DO UPDATE SET value=EXCLUDED.value,
		version=platform_settings.version+1, updated_at=now()
		WHERE platform_settings.value IS DISTINCT FROM EXCLUDED.value`, globalAgentsKey, data)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 0 {
		if _, err := tx.ExecContext(ctx, "UPDATE work_items SET context_version = context_version + 1, updated_at = now() WHERE state = 'open'"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE discord_conversations SET context_version = context_version + 1, updated_at = now()"); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Service) SaveAgentProvider(ctx context.Context, input AgentProviderInput) error {
	input.ModelSource = strings.TrimSpace(input.ModelSource)
	if input.ModelSource != ModelSourceChatGPT && input.ModelSource != ModelSourceProvider {
		return errors.New("模型来源必须是 chatgpt 或 provider")
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
	if input.ModelSource == ModelSourceProvider && credentialVersion == "" {
		return errors.New("选择 Provider 模式时必须配置 App Key")
	}
	if input.ModelSource == ModelSourceChatGPT && !old.ChatGPTConfigured {
		return errors.New("切换到 ChatGPT 模式前必须先完成全局登录")
	}
	record := providerRecord{AgentProvider: AgentProvider{
		ModelSource: input.ModelSource, BaseURL: strings.TrimRight(input.BaseURL, "/"),
		Model: input.Model, Reasoning: input.Reasoning, ServiceTier: input.ServiceTier,
		ProxyURL: input.ProxyURL, ProviderConfigured: credentialVersion != "",
		ChatGPTConfigured:   old.ChatGPTConfigured,
		ChatGPTAuthRevision: old.ChatGPTAuthRevision,
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

func (s *Service) SetChatGPTConfigured(ctx context.Context, configured bool) error {
	record, err := s.record(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if record.ModelSource == "" {
		record.ModelSource = ModelSourceProvider
	}
	if !configured && record.ModelSource == ModelSourceChatGPT {
		return errors.New("ChatGPT 模式下不能退出全局账号")
	}
	record.ChatGPTConfigured = configured
	record.ChatGPTAuthRevision++
	record.ConfigSignature = signature(record)
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `INSERT INTO platform_settings(setting_key, value)
		VALUES ($1,$2) ON CONFLICT(setting_key) DO UPDATE SET value=EXCLUDED.value,
		version=platform_settings.version+1, updated_at=now()`, agentProviderKey, data); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE work_items SET context_version=context_version+1,
		updated_at=now() WHERE state='open'`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE discord_conversations
		SET context_version=context_version+1, updated_at=now()`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) ChatGPTAuth(ctx context.Context, sharedHome string) ([]byte, int64, error) {
	provider, err := s.AgentProvider(ctx)
	if err != nil {
		return nil, 0, err
	}
	if !provider.ChatGPTConfigured {
		return nil, provider.ChatGPTAuthRevision, nil
	}
	auth, err := os.ReadFile(filepath.Join(sharedHome, "auth.json"))
	if err != nil {
		return nil, 0, err
	}
	return auth, provider.ChatGPTAuthRevision, nil
}

func (s *Service) PrepareCodexHome(ctx context.Context, codexHome, sharedHome string) (AgentProvider, []string, error) {
	provider, err := s.AgentProvider(ctx)
	if err != nil {
		return AgentProvider{}, nil, err
	}
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		return AgentProvider{}, nil, err
	}
	agents, err := s.GlobalAgents(ctx)
	if err != nil {
		return AgentProvider{}, nil, err
	}
	if err := WriteGlobalAgents(filepath.Join(codexHome, "AGENTS.md"), agents.Content); err != nil {
		return AgentProvider{}, nil, err
	}
	if err := SyncChatGPTAuth(codexHome, sharedHome, provider.ChatGPTConfigured,
		provider.ChatGPTAuthRevision); err != nil {
		return AgentProvider{}, nil, err
	}
	environment := make([]string, 0, 4)
	if provider.ModelSource == ModelSourceProvider {
		apiKey, err := s.APIKey(ctx)
		if err != nil {
			return AgentProvider{}, nil, err
		}
		environment = append(environment, "TYRS_HAND_MODEL_API_KEY="+string(apiKey))
	}
	if provider.ProxyURL != "" {
		environment = append(environment, "HTTP_PROXY="+provider.ProxyURL, "HTTPS_PROXY="+provider.ProxyURL)
	}
	return provider, environment, nil
}

func SyncChatGPTAuth(codexHome, sharedHome string, configured bool, revision int64) error {
	marker := filepath.Join(codexHome, ".tyrs-hand-chatgpt-auth-revision")
	expected := strconv.FormatInt(revision, 10)
	if current, err := os.ReadFile(marker); err == nil && string(current) == expected {
		return nil
	}
	authPath := filepath.Join(codexHome, "auth.json")
	if configured {
		sharedAuth, err := os.ReadFile(filepath.Join(sharedHome, "auth.json"))
		if err != nil {
			return errors.New("全局 ChatGPT 账号认证文件不存在")
		}
		if err := writeSecretFile(authPath, sharedAuth); err != nil {
			return err
		}
	} else if err := os.Remove(authPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeSecretFile(marker, []byte(expected))
}

func (s *Service) globalAgents(ctx context.Context) (GlobalAgents, bool, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT value FROM platform_settings WHERE setting_key=$1`,
		globalAgentsKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return GlobalAgents{}, false, nil
	}
	if err != nil {
		return GlobalAgents{}, false, err
	}
	var result GlobalAgents
	return result, true, json.Unmarshal(raw, &result)
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
	return old.ModelSource != current.ModelSource || old.BaseURL != current.BaseURL ||
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

func WriteGlobalAgents(path, content string) error {
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, []byte(content), 0o644); err != nil {
		return err
	}
	if err := os.Chmod(temporary, 0o644); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}
