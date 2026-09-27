package codex

// Worker 已支持 JSON Schema 表单；身份验证仍缺可信实现，不能一并声明支持。
func initializeCapabilities() map[string]any {
	return map[string]any{
		"experimentalApi":    true,
		"requestAttestation": false,
		"extensions": map[string]any{
			"openai/form":        map[string]any{},
			"openai/elicitation": map[string]any{"form": map[string]any{}},
		},
	}
}
