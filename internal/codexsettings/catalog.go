package codexsettings

import "slices"

type ReasoningEffortOption struct {
	ID          string `json:"id"`
	Description string `json:"description"`
}

type ServiceTierOption struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type ModelOption struct {
	ID                        string                  `json:"id"`
	DisplayName               string                  `json:"displayName"`
	Description               string                  `json:"description"`
	SupportedReasoningEfforts []ReasoningEffortOption `json:"supportedReasoningEfforts"`
	DefaultReasoningEffort    string                  `json:"defaultReasoningEffort"`
	InputModalities           []string                `json:"inputModalities"`
	ServiceTiers              []ServiceTierOption     `json:"serviceTiers"`
	DefaultServiceTier        string                  `json:"defaultServiceTier"`
	Default                   bool                    `json:"default"`
}

func ModelCatalog() []ModelOption {
	return []ModelOption{
		modelOption("gpt-5.6-sol", "GPT-5.6-Sol", "Latest frontier agentic coding model.",
			"low", []string{"low", "medium", "high", "xhigh", "max", "ultra"}, true),
		modelOption("gpt-5.6-terra", "GPT-5.6-Terra",
			"Balanced agentic coding model for everyday work.", "medium",
			[]string{"low", "medium", "high", "xhigh", "max", "ultra"}, false),
		modelOption("gpt-5.6-luna", "GPT-5.6-Luna", "Fast and affordable agentic coding model.",
			"medium", []string{"low", "medium", "high", "xhigh", "max"}, false),
		modelOption("gpt-5.5", "GPT-5.5",
			"Frontier model for complex coding, research, and real-world work.",
			"medium", []string{"low", "medium", "high", "xhigh"}, false),
		modelOption("gpt-5.2", "GPT-5.2",
			"Optimized for professional work and long-running agents.",
			"medium", []string{"low", "medium", "high", "xhigh"}, false),
	}
}

func modelOption(id, displayName, description, defaultEffort string, efforts []string,
	isDefault bool,
) ModelOption {
	result := ModelOption{ID: id, DisplayName: displayName, Description: description,
		DefaultReasoningEffort: defaultEffort, InputModalities: []string{"text", "image"},
		ServiceTiers: []ServiceTierOption{{ID: "standard", Name: "标准",
			Description: "标准响应速度与用量"}},
		DefaultServiceTier: "standard", Default: isDefault}
	for _, effort := range efforts {
		result.SupportedReasoningEfforts = append(result.SupportedReasoningEfforts,
			ReasoningEffortOption{ID: effort, Description: reasoningDescription(effort)})
	}
	if id != "gpt-5.2" {
		result.ServiceTiers = append(result.ServiceTiers, ServiceTierOption{ID: "fast",
			Name: "快速", Description: "更低延迟，增加用量"})
	}
	return result
}

func reasoningDescription(value string) string {
	switch value {
	case "low":
		return "较快响应与较轻推理"
	case "medium":
		return "平衡速度与推理深度"
	case "high":
		return "适合复杂任务的深入推理"
	case "xhigh":
		return "复杂任务的额外高强度推理"
	case "max":
		return "最困难任务的最大推理深度"
	case "ultra":
		return "最大推理并自动使用任务委派"
	default:
		return ""
	}
}

func ValidReasoningEffort(model, effort string) bool {
	if effort == "" {
		return true
	}
	for _, option := range ModelCatalog() {
		if option.ID != model {
			continue
		}
		return slices.ContainsFunc(option.SupportedReasoningEfforts,
			func(item ReasoningEffortOption) bool { return item.ID == effort })
	}
	return slices.Contains([]string{"low", "medium", "high", "xhigh", "max", "ultra"}, effort)
}

func DefaultModelOption() ModelOption {
	for _, option := range ModelCatalog() {
		if option.Default {
			return option
		}
	}
	return ModelCatalog()[0]
}
