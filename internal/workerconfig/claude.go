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

// ClaudeService 只操作该运行时的原生配置，不读取宿主个人登录态。
type ClaudeService struct {
	home    string
	mu      sync.Mutex
	restart func() error
}

func NewClaudeService(home string) *ClaudeService   { return &ClaudeService{home: home} }
func (s *ClaudeService) SetRestart(fn func() error) { s.restart = fn }
func (s *ClaudeService) Restart() error {
	if s.restart == nil {
		return errors.New("Claude 运行时尚未启用")
	}
	return s.restart()
}

type ClaudeProviderInput struct {
	Revision    string `json:"revision"`
	BaseURL     string `json:"baseUrl"`
	APIKey      string `json:"apiKey"`
	ClearAPIKey bool   `json:"clearApiKey"`
	AuthMethod  string `json:"authMethod"`
	Model       string `json:"model"`
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
			return result, nil, errors.New("Claude settings.json 必须是有效 JSON 对象")
		}
	}
	env, err := claudeSettingsEnv(settings)
	if err != nil {
		return result, nil, err
	}
	agents, err := readOptional(filepath.Join(s.home, "CLAUDE.md"))
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
	if raw, ok := settings["model"]; ok {
		if json.Unmarshal(raw, &result.Model) != nil {
			return result, nil, errors.New("Claude model 必须是字符串")
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
			return nil, errors.New("Claude settings.env 必须是字符串字典")
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
	if err := writeWithBackups(filepath.Join(s.home, "CLAUDE.md"), []byte(content)); err != nil {
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
	input.BaseURL = strings.TrimSpace(input.BaseURL)
	u, err := url.Parse(input.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return current, errors.New("Base URL 必须是无凭据、查询参数和片段的 HTTP/HTTPS URL")
	}
	if len(input.BaseURL) > 2048 || len(input.APIKey) > 4096 || len(input.Model) > 256 {
		return current, errors.New("Provider 配置长度超限")
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
		return current, errors.New("Model Provider 认证方式无效")
	}
	if input.ClearAPIKey && input.APIKey != "" {
		return current, errors.New("API Key 不能同时设置与清除")
	}
	env, _ := claudeSettingsEnv(settings)
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
