package codex

import "strconv"

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

// HomeAppServerArguments 只指定传输地址，其余 Codex 配置全部由真实 CODEX_HOME 决定。
func HomeAppServerArguments(listen string) []string {
	return []string{"app-server", "--listen", listen}
}

// ManagedAppServerArguments 固定平台认证边界，其他个人配置仍从 CODEX_HOME 读取。
func ManagedAppServerArguments(listen string, config ManagedAppServerConfig) []string {
	arguments := []string{
		"--config", `shell_environment_policy.inherit="core"`,
		"--config", "shell_environment_policy.ignore_default_excludes=false",
		"--config", `shell_environment_policy.exclude=["TYRS_HAND_MODEL_API_KEY","` +
			BrowserMCPWorkerTokenEnvironment + `","` + BrowserMCPDesktopTokenEnvironment + `"]`,
		"--config", "allow_login_shell=false",
		"--config", `openai_base_url="` + chatGPTCodexBaseURL + `"`,
	}
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

func tomlString(key, value string) string {
	return key + "=" + strconv.Quote(value)
}
