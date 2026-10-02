package workerconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

// ClaudeService 操作 Worker 用户宿主 ~/.claude 的原生配置。
type ClaudeService struct {
	home    string
	mu      sync.Mutex
	restart func() error
	// sharedAgents 非空时，全局指令与 Codex 共用该 AGENTS.md，CLAUDE.md 只是指向它的软链。
	sharedAgents string
}

func NewClaudeService(home string) *ClaudeService   { return &ClaudeService{home: home} }
func (s *ClaudeService) SetRestart(fn func() error) { s.restart = fn }

// NewSharedClaudeService 创建与 Codex 共用全局指令的 Claude 配置服务：读写都作用于同一 AGENTS.md。
func NewSharedClaudeService(home, codexAgents string) *ClaudeService {
	return &ClaudeService{home: home, sharedAgents: codexAgents}
}

func (s *ClaudeService) instructionsPath() string {
	if s.sharedAgents != "" {
		return s.sharedAgents
	}
	return filepath.Join(s.home, "CLAUDE.md")
}

// ShareClaudeInstructions 让 Claude 的 CLAUDE.md 成为 Codex AGENTS.md 的软链，两引擎共用一份全局指令。
// 既有独立 CLAUDE.md 的内容不丢：AGENTS.md 缺失或为空时迁入，否则另存为 CLAUDE.md.pre-shared。
func ShareClaudeInstructions(claudeHome, codexAgents string) error {
	if err := os.MkdirAll(claudeHome, 0o700); err != nil {
		return err
	}
	link := filepath.Join(claudeHome, "CLAUDE.md")
	info, err := os.Lstat(link)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return err
	case info.Mode()&os.ModeSymlink != 0:
		if target, _ := os.Readlink(link); target == codexAgents {
			return nil
		}
		if err := os.Remove(link); err != nil {
			return err
		}
	default:
		content, err := os.ReadFile(link)
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(content)) != "" {
			existing, err := readOptional(codexAgents)
			if err != nil {
				return err
			}
			destination := link + ".pre-shared"
			if strings.TrimSpace(string(existing)) == "" {
				destination = codexAgents
			}
			if err := atomicWrite(destination, content, 0o600); err != nil {
				return err
			}
		}
		if err := os.Remove(link); err != nil {
			return err
		}
	}
	return os.Symlink(codexAgents, link)
}

// claudeDefaultEnv 是 Worker 每次启动时强制写入宿主 settings.json 的 env 默认值。
var claudeDefaultEnv = map[string]string{"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1"}

// 与 Provider 原子保存，避免开关与凭据清理只成功一半；原生 settings.env 允许自定义变量。
const claudeProviderSyncEnv = "TYRS_HAND_CLAUDE_PROVIDER_SYNC"

// ApplyClaudeDefaultSettings 只覆盖默认 env 键，保留其他设置；已一致时不写文件也不产生备份。
func ApplyClaudeDefaultSettings(claudeHome string) error {
	path := filepath.Join(claudeHome, "settings.json")
	data, err := readOptional(path)
	if err != nil {
		return err
	}
	settings := map[string]json.RawMessage{}
	if len(data) > 0 {
		if json.Unmarshal(data, &settings) != nil || settings == nil {
			return errors.New("必须为 Claude settings.json 提供有效 JSON 对象")
		}
	}
	env, err := claudeSettingsEnv(settings)
	if err != nil {
		return err
	}
	changed := false
	for name, value := range claudeDefaultEnv {
		if env[name] != value {
			env[name], changed = value, true
		}
	}
	if !changed {
		return nil
	}
	settings["env"], _ = json.Marshal(env)
	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(claudeHome, 0o700); err != nil {
		return err
	}
	return writeWithBackups(path, append(encoded, '\n'))
}

func (s *ClaudeService) Restart() error {
	if s.restart == nil {
		return errors.New("尚未启用 Claude 运行时")
	}
	return s.restart()
}

type ClaudeProviderInput struct {
	ProviderSyncEnabled *bool  `json:"providerSyncEnabled,omitempty"`
	Revision            string `json:"revision"`
	BaseURL             string `json:"baseUrl"`
	APIKey              string `json:"apiKey"`
	ClearAPIKey         bool   `json:"clearApiKey"`
	AuthMethod          string `json:"authMethod"`
	Model               string `json:"model"`
}

func (s *ClaudeService) Read() (workerprotocol.WorkerConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, _, err := s.read()
	return current, err
}

func (s *ClaudeService) read() (workerprotocol.WorkerConfig, map[string]json.RawMessage, error) {
	var result workerprotocol.WorkerConfig
	data, err := readOptional(filepath.Join(s.home, "settings.json"))
	if err != nil {
		return result, nil, err
	}
	settings := map[string]json.RawMessage{}
	if len(data) > 0 {
		if json.Unmarshal(data, &settings) != nil || settings == nil {
			return result, nil, errors.New("必须为 Claude settings.json 提供有效 JSON 对象")
		}
	}
	env, err := claudeSettingsEnv(settings)
	if err != nil {
		return result, nil, err
	}
	agents, err := readOptional(s.instructionsPath())
	if err != nil {
		return result, nil, err
	}
	result.Revision, result.Agents = revision(data, agents), string(agents)
	result.BaseURL = env["ANTHROPIC_BASE_URL"]
	result.AuthMethod, result.EnvKey = "api-key", "ANTHROPIC_API_KEY"
	if env["ANTHROPIC_AUTH_TOKEN"] != "" {
		result.AuthMethod, result.EnvKey = "auth-token", "ANTHROPIC_AUTH_TOKEN"
	}
	result.APIKeyConfigured = env[result.EnvKey] != ""
	enabled := env[claudeProviderSyncEnv] != "0"
	result.ProviderSyncEnabled = &enabled
	if raw, ok := settings["model"]; ok {
		if json.Unmarshal(raw, &result.Model) != nil {
			return result, nil, errors.New("必须为 Claude model 提供字符串")
		}
	}
	if env["ANTHROPIC_MODEL"] != "" {
		result.Model = env["ANTHROPIC_MODEL"]
	}
	return result, settings, nil
}

func claudeSettingsEnv(settings map[string]json.RawMessage) (map[string]string, error) {
	env := map[string]string{}
	if raw, ok := settings["env"]; ok {
		if json.Unmarshal(raw, &env) != nil || env == nil {
			return nil, errors.New("必须为 Claude settings.env 提供字符串字典")
		}
	}
	return env, nil
}

func (s *ClaudeService) UpdateAgents(expected, content string) (workerprotocol.WorkerConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, _, err := s.read()
	if err != nil {
		return current, err
	}
	if expected == "" || expected != current.Revision {
		return current, errors.New("配置版本冲突")
	}
	if len(content) > 1024*1024 {
		return current, errors.New("配置长度超限")
	}
	// 共用时与 Codex 一致直接原子写入 AGENTS.md，不在 Codex Home 留下 Claude 的备份文件。
	write := writeWithBackups
	if s.sharedAgents != "" {
		write = func(path string, data []byte) error { return atomicWrite(path, data, 0o600) }
	}
	if err := write(s.instructionsPath(), []byte(content)); err != nil {
		return current, err
	}
	current, _, err = s.read()
	return current, err
}

func (s *ClaudeService) UpdateProvider(input ClaudeProviderInput) (workerprotocol.WorkerConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, settings, err := s.read()
	if err != nil {
		return current, err
	}
	if input.Revision == "" || input.Revision != current.Revision {
		return current, errors.New("配置版本冲突")
	}
	if input.ProviderSyncEnabled != nil && !*input.ProviderSyncEnabled {
		if !*current.ProviderSyncEnabled {
			return current, nil // 已关闭时不再改写用户后来设置的原生 Provider。
		}
		env, _ := claudeSettingsEnv(settings)
		for _, name := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "ANTHROPIC_MODEL"} {
			delete(env, name)
		}
		delete(settings, "model")
		env[claudeProviderSyncEnv] = "0"
		settings["env"], _ = json.Marshal(env)
		return s.writeSettings(settings)
	}
	if !*current.ProviderSyncEnabled && input.ProviderSyncEnabled == nil {
		return current, errors.New("当前 Model Provider 同步已关闭，请先显式启用")
	}
	input.BaseURL = strings.TrimSpace(input.BaseURL)
	u, err := url.Parse(input.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return current, errors.New("必须为 Base URL 提供无凭据、查询参数和片段的 HTTP/HTTPS URL")
	}
	if len(input.BaseURL) > 2048 || len(input.APIKey) > 4096 || len(input.Model) > 256 {
		return current, errors.New("超过 Provider 配置长度限制")
	}
	if strings.ContainsAny(input.APIKey+input.Model, "\r\n\x00") {
		return current, errors.New("API Key 或 Model 包含非法字符")
	}
	key := "ANTHROPIC_API_KEY"
	switch input.AuthMethod {
	case "api-key":
	case "auth-token":
		key = "ANTHROPIC_AUTH_TOKEN"
	default:
		return current, errors.New("无效的 Model Provider 认证方式")
	}
	if input.ClearAPIKey && input.APIKey != "" {
		return current, errors.New("API Key 不能同时设置与清除")
	}
	env, _ := claudeSettingsEnv(settings)
	delete(env, claudeProviderSyncEnv)
	secret := env[key]
	if input.APIKey != "" {
		secret = input.APIKey
	}
	if !input.ClearAPIKey && secret == "" {
		return current, errors.New("当前认证方式必须填写 API Key")
	}
	delete(env, "ANTHROPIC_API_KEY")
	delete(env, "ANTHROPIC_AUTH_TOKEN")
	if !input.ClearAPIKey {
		env[key] = secret
	}
	env["ANTHROPIC_BASE_URL"] = input.BaseURL
	// 避免原有环境变量继续覆盖控制台保存的原生 model 设置。
	delete(env, "ANTHROPIC_MODEL")
	delete(settings, "model")
	if input.Model != "" {
		settings["model"], _ = json.Marshal(input.Model)
	}
	settings["env"], _ = json.Marshal(env)
	return s.writeSettings(settings)
}

func (s *ClaudeService) writeSettings(settings map[string]json.RawMessage) (workerprotocol.WorkerConfig, error) {
	var current workerprotocol.WorkerConfig
	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return current, err
	}
	if err := writeWithBackups(filepath.Join(s.home, "settings.json"), append(encoded, '\n')); err != nil {
		return current, err
	}
	current, _, err = s.read()
	return current, err
}

func readOptional(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

// 修改前保留四个历史版本；备份失败时不覆盖现配置，备份同样为 0600。
func writeWithBackups(path string, data []byte) error {
	previous, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		for index := 3; index >= 1; index-- {
			old, readErr := os.ReadFile(fmt.Sprintf("%s.bak.%d", path, index))
			if errors.Is(readErr, os.ErrNotExist) {
				continue
			}
			if readErr != nil {
				return readErr
			}
			if err := atomicWrite(fmt.Sprintf("%s.bak.%d", path, index+1), old, 0o600); err != nil {
				return err
			}
		}
		if err := atomicWrite(path+".bak.1", previous, 0o600); err != nil {
			return err
		}
	}
	return atomicWrite(path, data, 0o600)
}
