package codex

const chatGPTCodexBaseURL = "https://chatgpt.com/backend-api/codex"

// ManagedAppServerArguments 固定平台认证边界，其他个人配置仍从 CODEX_HOME 读取。
func ManagedAppServerArguments(listen string) []string {
	return []string{
		"--config", `shell_environment_policy.inherit="core"`,
		"--config", "shell_environment_policy.ignore_default_excludes=false",
		"--config", `shell_environment_policy.exclude=["TYRS_HAND_MODEL_API_KEY"]`,
		"--config", "allow_login_shell=false",
		"--config", `openai_base_url="` + chatGPTCodexBaseURL + `"`,
		"app-server", "--listen", listen,
	}
}
