package workerconfig

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

var deviceCodePattern = regexp.MustCompile(`(?m)\b([A-Z0-9]{4}-[A-Z0-9]{4})\b`)

type Service struct {
	home          string
	codexBin      string
	stateDir      string
	envFile       string
	workspaceRoot string
	restart       func() error
	mu            sync.Mutex
	oauth         *oauthProcess
}

type oauthProcess struct {
	cmd       *exec.Cmd
	done      chan error
	device    workerprotocol.OAuthDevice
	startedAt time.Time
	finished  bool
}

func NewService(home, codexBin string) *Service {
	return NewServiceWithStateDir(home, codexBin, filepath.Dir(filepath.Clean(home)))
}

func NewServiceWithStateDir(home, codexBin, stateDir string) *Service {
	return NewServiceWithStateDirAndEnv(home, codexBin, stateDir, "")
}

func NewServiceWithStateDirAndEnv(home, codexBin, stateDir, envFile string) *Service {
	if strings.TrimSpace(codexBin) == "" {
		codexBin = "codex"
	}
	if strings.TrimSpace(stateDir) == "" {
		stateDir = filepath.Dir(filepath.Clean(home))
	}
	return &Service{home: filepath.Clean(home), codexBin: codexBin, stateDir: filepath.Clean(stateDir), envFile: filepath.Clean(envFile)}
}

func (s *Service) SetRestart(fn func() error) { s.restart = fn }

func (s *Service) SetWorkspaceRoot(root string) {
	s.workspaceRoot = filepath.Clean(root)
}

func (s *Service) Restart() error {
	if s.restart == nil {
		return errors.New("Worker 尚未绑定 Codex 重启处理器")
	}
	return s.restart()
}

func (s *Service) Read() (workerprotocol.WorkerConfig, error) {
	configPath := filepath.Join(s.home, "config.toml")
	data, err := os.ReadFile(configPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return workerprotocol.WorkerConfig{}, err
	}
	var parsed map[string]any
	if len(data) > 0 {
		if err := toml.Unmarshal(data, &parsed); err != nil {
			return workerprotocol.WorkerConfig{}, fmt.Errorf("读取 Codex config.toml: %w", err)
		}
	}
	provider, _ := parsed["model_provider"].(string)
	if isChatGPTProvider(provider) {
		return workerprotocol.WorkerConfig{}, errors.New("Model Provider 不得使用 ChatGPT OAuth")
	}
	providers := map[string]any{}
	if value, ok := parsed["model_providers"].(map[string]any); ok {
		for id, raw := range value {
			if item, ok := raw.(map[string]any); ok {
				copy := map[string]any{}
				for _, key := range []string{"name", "base_url", "wire_api", "env_key", "env_http_headers", "query_params", "requires_openai_auth"} {
					if value, exists := item[key]; exists {
						copy[key] = value
					}
				}
				if baseURL, ok := copy["base_url"].(string); ok && isChatGPTProvider(baseURL) {
					return workerprotocol.WorkerConfig{}, errors.New("Model Provider 不得使用 ChatGPT OAuth")
				}
				providers[id] = copy
			}
		}
	}
	agents, err := os.ReadFile(filepath.Join(s.home, "AGENTS.md"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return workerprotocol.WorkerConfig{}, err
	}
	baseURL, envKey := "", ""
	if item, ok := providers[provider].(map[string]any); ok {
		baseURL, _ = item["base_url"].(string)
		envKey, _ = item["env_key"].(string)
	}
	if envKey == "" {
		envKey = modelAPIKeyEnv
	}
	apiKeyConfigured := false
	apiKeyDigest := ""
	if envData, readErr := os.ReadFile(s.globalEnvPath()); readErr == nil {
		for _, line := range strings.Split(string(envData), "\n") {
			name, value, ok := cutEnvLine(line)
			if ok && name == modelAPIKeyEnv && strings.TrimSpace(value) != "" {
				apiKeyConfigured = true
				digest := sha256.Sum256([]byte(value))
				apiKeyDigest = hex.EncodeToString(digest[:])
				break
			}
		}
	}
	revisionData := append([]byte{}, data...)
	if apiKeyConfigured {
		revisionData = append(revisionData, []byte("\x00api-key:"+apiKeyDigest)...)
	}
	return workerprotocol.WorkerConfig{Revision: revision(revisionData, agents), ModelProvider: provider, ModelProviders: providers, BaseURL: baseURL, EnvKey: envKey, APIKeyConfigured: apiKeyConfigured, Agents: string(agents)}, nil
}

const modelAPIKeyEnv = "TYRS_HAND_MODEL_API_KEY"
const modelBaseURLEnv = "TYRS_HAND_MODEL_BASE_URL"

func (s *Service) globalEnvPath() string {
	if s.envFile != "" {
		return s.envFile
	}
	return filepath.Join(s.stateDir, ".env")
}

func isChatGPTProvider(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.Contains(value, "chatgpt.com") || strings.Contains(value, "chatgpt")
}

func (s *Service) UpdateAgents(expected, content string) (workerprotocol.WorkerConfig, error) {
	current, err := s.Read()
	if err != nil {
		return workerprotocol.WorkerConfig{}, err
	}
	if expected != "" && expected != current.Revision {
		return workerprotocol.WorkerConfig{}, fmt.Errorf("配置版本冲突")
	}
	path := filepath.Join(s.home, "AGENTS.md")
	if err := atomicWrite(path, []byte(content), 0o600); err != nil {
		return workerprotocol.WorkerConfig{}, err
	}
	return s.Read()
}

func (s *Service) UpdateProvider(expected, baseURL, apiKey string, clearAPIKey bool) (workerprotocol.WorkerConfig, error) {
	current, err := s.Read()
	if err != nil {
		return workerprotocol.WorkerConfig{}, err
	}
	if expected != "" && expected != current.Revision {
		return workerprotocol.WorkerConfig{}, errors.New("配置版本冲突")
	}
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return workerprotocol.WorkerConfig{}, errors.New("Base URL 不能为空")
	}
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return workerprotocol.WorkerConfig{}, errors.New("Base URL 必须是合法的 HTTP/HTTPS URL")
	}
	if len(baseURL) > 2048 || len(apiKey) > 4096 {
		return workerprotocol.WorkerConfig{}, errors.New("Provider 配置长度超限")
	}
	if strings.ContainsAny(apiKey, "\r\n'") {
		return workerprotocol.WorkerConfig{}, errors.New("API Key 包含非法换行符")
	}
	if isChatGPTProvider(baseURL) {
		return workerprotocol.WorkerConfig{}, errors.New("Model Provider 不得使用 ChatGPT OAuth")
	}
	if current.ModelProvider == "" && !current.APIKeyConfigured && (strings.TrimSpace(apiKey) == "" || clearAPIKey) {
		return workerprotocol.WorkerConfig{}, errors.New("首次配置必须填写 API Key")
	}
	path := filepath.Join(s.home, "config.toml")
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return workerprotocol.WorkerConfig{}, err
	}
	parsed := map[string]any{}
	if len(data) > 0 {
		if err := toml.Unmarshal(data, &parsed); err != nil {
			return workerprotocol.WorkerConfig{}, err
		}
	}
	provider := current.ModelProvider
	if provider == "" {
		provider = "tyrs-hand-provider"
	}
	parsed["model_provider"] = provider
	providers, _ := parsed["model_providers"].(map[string]any)
	if providers == nil {
		providers = map[string]any{}
	}
	item, _ := providers[provider].(map[string]any)
	if item == nil {
		item = map[string]any{}
	}
	item["name"] = "Tyrs Hand Provider"
	item["base_url"] = baseURL
	item["wire_api"] = "responses"
	item["env_key"] = modelAPIKeyEnv
	item["requires_openai_auth"] = true
	providers[provider] = item
	parsed["model_providers"] = providers
	encoded, err := toml.Marshal(parsed)
	if err != nil {
		return workerprotocol.WorkerConfig{}, err
	}
	if err := atomicWrite(path, encoded, 0o600); err != nil {
		return workerprotocol.WorkerConfig{}, err
	}
	if err := s.updateGlobalEnv(baseURL, apiKey, clearAPIKey); err != nil {
		return workerprotocol.WorkerConfig{}, err
	}
	return s.Read()
}

func (s *Service) updateGlobalEnv(baseURL, apiKey string, clear bool) error {
	path := s.globalEnvPath()
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	foundAPIKey, foundBaseURL := false, false
	result := make([]string, 0, len(lines)+1)
	for _, line := range lines {
		name, value, ok := cutEnvLine(line)
		if ok && (name == modelAPIKeyEnv || name == modelBaseURLEnv) {
			if name == modelAPIKeyEnv && !clear && apiKey != "" {
				foundAPIKey = true
				result = append(result, modelAPIKeyEnv+"="+apiKey)
			} else if name == modelAPIKeyEnv && !clear {
				foundAPIKey = true
				result = append(result, modelAPIKeyEnv+"="+value)
			} else if name == modelAPIKeyEnv {
				foundAPIKey = true
			}
			if name == modelBaseURLEnv {
				foundBaseURL = true
				result = append(result, modelBaseURLEnv+"="+baseURL)
			}
			continue
		}
		result = append(result, line)
	}
	if !clear && apiKey != "" && !foundAPIKey {
		result = append(result, modelAPIKeyEnv+"="+apiKey)
	}
	if !foundBaseURL {
		result = append(result, modelBaseURLEnv+"="+baseURL)
	}
	return writeGlobalEnv(path, []byte(strings.Join(result, "\n")+"\n"))
}

// writeGlobalEnv 更新 /etc/environment 一类的机器级环境文件。该文件由安装器
// 预先创建并授予 Worker 用户写权限，以便非 root Worker 也能保存 Provider。
func writeGlobalEnv(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o664)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	return file.Sync()
}

func cutEnvLine(line string) (string, string, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
	for index := range line {
		if line[index] == '=' {
			return line[:index], unquoteEnvValue(strings.TrimSpace(line[index+1:])), true
		}
	}
	return "", "", false
}

func unquoteEnvValue(value string) string {
	if len(value) >= 2 && ((value[0] == '\'' && value[len(value)-1] == '\'') || (value[0] == '"' && value[len(value)-1] == '"')) {
		return value[1 : len(value)-1]
	}
	return value
}

func (s *Service) StartOAuth() (workerprotocol.OAuthDevice, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.oauth != nil && s.oauth.device.Status == "pending" {
		return s.oauth.device, nil
	}
	cmd := exec.Command(s.codexBin, "login", "--device-auth")
	cmd.Dir = s.home
	cmd.Env = append(os.Environ(), "HOME="+filepath.Dir(s.home), "CODEX_HOME="+s.home)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return workerprotocol.OAuthDevice{}, err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return workerprotocol.OAuthDevice{}, err
	}
	lines := make(chan string, 32)
	done := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()
	go func() { done <- cmd.Wait() }()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	device := workerprotocol.OAuthDevice{VerificationURL: "https://auth.openai.com/codex/device", ExpiresAt: time.Now().UTC().Add(15 * time.Minute), Status: "pending"}
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				return workerprotocol.OAuthDevice{}, errors.New("Codex OAuth 未返回设备码")
			}
			if match := deviceCodePattern.FindStringSubmatch(strings.ToUpper(line)); len(match) == 2 {
				device.UserCode = match[1]
				s.oauth = &oauthProcess{cmd: cmd, done: done, device: device, startedAt: time.Now().UTC()}
				return device, nil
			}
		case err := <-done:
			return workerprotocol.OAuthDevice{}, fmt.Errorf("Codex OAuth 进程退出: %w", err)
		case <-deadline.C:
			_ = cmd.Process.Kill()
			return workerprotocol.OAuthDevice{}, errors.New("等待 Codex OAuth 设备码超时")
		}
	}
}

func (s *Service) OAuthStatus() workerprotocol.OAuthDevice {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.oauth == nil {
		if _, err := os.Stat(filepath.Join(s.home, "auth.json")); err == nil {
			return workerprotocol.OAuthDevice{Status: "authenticated"}
		}
		return workerprotocol.OAuthDevice{Status: "logged_out"}
	}
	if !s.oauth.finished {
		select {
		case <-s.oauth.done:
			s.oauth.finished = true
			if _, err := os.Stat(filepath.Join(s.home, "auth.json")); err == nil {
				s.oauth.device.Status = "authenticated"
			} else {
				s.oauth.device.Status = "failed"
			}
		default:
		}
	}
	return s.oauth.device
}

func (s *Service) Logout() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cmd := exec.Command(s.codexBin, "logout")
	cmd.Dir = s.home
	cmd.Env = append(os.Environ(), "HOME="+filepath.Dir(s.home), "CODEX_HOME="+s.home)
	err := cmd.Run()
	if err == nil {
		s.oauth = nil
	}
	return err
}

func revision(config, agents []byte) string {
	hash := sha256.New()
	_, _ = hash.Write(config)
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(agents)
	return hex.EncodeToString(hash.Sum(nil))
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tyrs-hand-config-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
