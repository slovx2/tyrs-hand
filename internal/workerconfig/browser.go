package workerconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/pelletier/go-toml/v2"
)

var browserConfigMu sync.Mutex

// BrowserRegistration 只在宿主进程内传递派生令牌，不进入任务配置或诊断。
type BrowserRegistration struct {
	Home, CodexHome, PiAgentDir, StateDir string
	URL, Token                            string
}

// RegisterBrowserMCP 将同一宿主浏览器注册到三个原生用户配置。
// 各配置独立处理；冲突不覆盖，也不影响其他引擎的注册。
func RegisterBrowserMCP(options BrowserRegistration) error {
	browserConfigMu.Lock()
	defer browserConfigMu.Unlock()
	if options.URL == "" || options.Token == "" {
		return errors.New("浏览器注册缺少地址或派生令牌")
	}
	if options.PiAgentDir == "" {
		options.PiAgentDir = filepath.Join(options.Home, ".pi", "agent")
	}
	manifestPath := filepath.Join(options.StateDir, "browser-mcp-registrations.json")
	manifest := map[string]string{}
	data, err := readOptional(manifestPath)
	if err != nil {
		return fmt.Errorf("读取浏览器注册记录失败: %w", err)
	}
	if len(data) > 0 && (json.Unmarshal(data, &manifest) != nil || manifest == nil) {
		return errors.New("浏览器注册记录格式无效")
	}
	targets := []struct{ path, engine string }{
		{filepath.Join(options.CodexHome, "config.toml"), "codex"},
		{filepath.Join(options.Home, ".claude.json"), "claude"},
		{filepath.Join(options.PiAgentDir, "mcp.json"), "pi"},
	}
	var failures []error
	for _, target := range targets {
		if err := registerBrowserFile(target.path, target.engine, manifest[target.path], options); err != nil {
			failures = append(failures, fmt.Errorf("%s 用户级浏览器注册失败: %w", target.engine, err))
			continue
		}
		manifest[target.path] = options.URL
	}
	encoded, _ := json.MarshalIndent(manifest, "", "  ")
	encoded = append(encoded, '\n')
	if !bytes.Equal(data, encoded) {
		if err := writeWithBackups(manifestPath, encoded); err != nil {
			failures = append(failures, fmt.Errorf("保存浏览器注册记录失败: %w", err))
		}
	}
	return errors.Join(failures...)
}

func registerBrowserFile(path, engine, previousURL string, options BrowserRegistration) error {
	data, err := readOptional(path)
	if err != nil {
		return err
	}
	config := map[string]any{}
	if len(data) > 0 {
		if engine == "codex" {
			err = toml.Unmarshal(data, &config)
		} else {
			err = json.Unmarshal(data, &config)
		}
		// 解析错误可能带上包含凭据的源文本，不能透传。
		if err != nil || config == nil {
			return errors.New("用户配置格式无效，未修改原文件")
		}
	}
	key, headerKey := "mcpServers", "headers"
	if engine == "codex" {
		key, headerKey = "mcp_servers", "http_headers"
	}
	servers, err := browserConfigObject(config, key)
	if err != nil {
		return err
	}
	entry, err := browserConfigObject(servers, "chrome")
	if err != nil {
		return err
	}
	if len(entry) > 0 {
		url, _ := entry["url"].(string)
		if url == "" || (url != options.URL && url != previousURL) {
			return errors.New("chrome 已由其他服务占用，未修改原条目")
		}
	}
	before, _ := json.Marshal(config)
	entry["url"] = options.URL
	delete(entry, "bearer_token_env_var")
	delete(entry, "command")
	delete(entry, "args")
	headers, err := browserConfigObject(entry, headerKey)
	if err != nil {
		return err
	}
	headers["Authorization"] = "Bearer " + options.Token
	delete(headers, "X-Tyrs-Browser-Task-Id")
	entry[headerKey] = headers
	// 避免旧环境变量请求头覆盖静态凭据或固定任务标识。
	if envHeaders, ok := entry["env_http_headers"].(map[string]any); ok {
		delete(envHeaders, "Authorization")
		delete(envHeaders, "X-Tyrs-Browser-Task-Id")
	}
	if engine == "codex" {
		entry["startup_timeout_sec"] = 10
		entry["tool_timeout_sec"] = 120
	} else {
		entry["type"] = "http"
		if engine == "pi" {
			entry["timeout"] = 120
			entry["exposure"] = "direct"
		} else {
			entry["timeout"] = 120000
		}
	}
	servers["chrome"], config[key] = entry, servers
	after, _ := json.Marshal(config)
	if bytes.Equal(before, after) {
		return os.Chmod(path, 0o600)
	}
	var encoded []byte
	if engine == "codex" {
		encoded, err = toml.Marshal(config)
	} else {
		encoded, err = json.MarshalIndent(config, "", "  ")
		encoded = append(encoded, '\n')
	}
	if err != nil {
		return errors.New("无法编码用户级 MCP 配置")
	}
	// Claude 等客户端可能同时保存用户配置，检测变化后留待下次启动重试。
	current, err := readOptional(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, data) {
		return errors.New("用户配置已被其他进程修改，请重试")
	}
	return writeWithBackups(path, encoded)
}

func browserConfigObject(parent map[string]any, key string) (map[string]any, error) {
	value, exists := parent[key]
	if !exists {
		return map[string]any{}, nil
	}
	object, ok := value.(map[string]any)
	if !ok || object == nil {
		return nil, fmt.Errorf("%s 必须为配置对象", key)
	}
	return object, nil
}
