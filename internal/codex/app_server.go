package codex

import (
	"strconv"
	"strings"
)

const chatGPTCodexBaseURL = "https://chatgpt.com/backend-api/codex"

const (
	BrowserMCPWorkerTokenEnvironment  = "TYRS_BROWSER_MCP_WORKER_TOKEN"
	BrowserMCPDesktopTokenEnvironment = "TYRS_BROWSER_MCP_DESKTOP_TOKEN"
)

type ManagedModelProvider struct {
	ID                 string
	Name               string
	BaseURL            string
	WireAPI            string
	EnvKey             string
	RequiresOpenAIAuth bool
}

type ManagedAppServerConfig struct {
	ModelProvider ManagedModelProvider
}

// HomeAppServerArguments 保留真实 CODEX_HOME 配置，同时强制隔离宿主管理的 Browser Token。
func HomeAppServerArguments(listen string) []string {
	arguments := shellIsolationArguments(
		BrowserMCPWorkerTokenEnvironment,
		BrowserMCPDesktopTokenEnvironment,
	)
	return append(arguments, "app-server", "--listen", listen)
}

// ManagedAppServerArguments 固定平台认证边界，其他个人配置仍从 CODEX_HOME 读取。
func ManagedAppServerArguments(listen string, config ManagedAppServerConfig) []string {
	arguments := shellIsolationArguments("TYRS_HAND_MODEL_API_KEY",
		BrowserMCPWorkerTokenEnvironment, BrowserMCPDesktopTokenEnvironment)
	arguments = append(arguments, "--config", `openai_base_url="`+chatGPTCodexBaseURL+`"`)
	provider := config.ModelProvider
	if provider.ID != "" {
		arguments = append(arguments, "--config", tomlString("model_provider", provider.ID))
	}
	if provider.ID != "" && provider.BaseURL != "" {
		prefix := "model_providers." + provider.ID + "."
		arguments = append(arguments,
			"--config", tomlString(prefix+"name", provider.Name),
			"--config", tomlString(prefix+"base_url", provider.BaseURL),
			"--config", tomlString(prefix+"wire_api", provider.WireAPI),
			"--config", tomlString(prefix+"env_key", provider.EnvKey),
			"--config", prefix+"requires_openai_auth="+
				strconv.FormatBool(provider.RequiresOpenAIAuth),
		)
	}
	return append(arguments, "app-server", "--listen", listen)
}

func shellIsolationArguments(excluded ...string) []string {
	quoted := make([]string, 0, len(excluded))
	for _, name := range excluded {
		quoted = append(quoted, strconv.Quote(name))
	}
	return []string{
		"--config", `shell_environment_policy.inherit="core"`,
		"--config", "shell_environment_policy.ignore_default_excludes=false",
		"--config", "shell_environment_policy.exclude=[" + strings.Join(quoted, ",") + "]",
		"--config", "allow_login_shell=false",
	}
}

func tomlString(key, value string) string {
	return key + "=" + strconv.Quote(value)
}
