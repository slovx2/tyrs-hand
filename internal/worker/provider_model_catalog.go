package worker

import "encoding/json"

type providerDesktopReasoningEffort struct {
	ReasoningEffort string `json:"reasoningEffort"`
	Description     string `json:"description"`
}

type providerDesktopServiceTier struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type providerDesktopModel struct {
	ID                        string                           `json:"id"`
	Model                     string                           `json:"model"`
	Upgrade                   any                              `json:"upgrade"`
	UpgradeInfo               any                              `json:"upgradeInfo"`
	AvailabilityNux           any                              `json:"availabilityNux"`
	DisplayName               string                           `json:"displayName"`
	Description               string                           `json:"description"`
	Hidden                    bool                             `json:"hidden"`
	SupportedReasoningEfforts []providerDesktopReasoningEffort `json:"supportedReasoningEfforts"`
	DefaultReasoningEffort    string                           `json:"defaultReasoningEffort"`
	InputModalities           []string                         `json:"inputModalities"`
	SupportsPersonality       bool                             `json:"supportsPersonality"`
	AdditionalSpeedTiers      []string                         `json:"additionalSpeedTiers"`
	ServiceTiers              []providerDesktopServiceTier     `json:"serviceTiers"`
	DefaultServiceTier        any                              `json:"defaultServiceTier"`
	IsDefault                 bool                             `json:"isDefault"`
}

// providerDesktopModelCatalog 对齐固定 Codex 0.145.0 的无登录 Provider 模型目录。
func providerDesktopModelCatalog() (json.RawMessage, error) {
	models := []providerDesktopModel{
		providerModel(
			"gpt-5.6-sol", "GPT-5.6-Sol", "Latest frontier agentic coding model.",
			"low", []string{"low", "medium", "high", "xhigh", "max", "ultra"}, true,
		),
		providerModel(
			"gpt-5.6-terra", "GPT-5.6-Terra",
			"Balanced agentic coding model for everyday work.",
			"medium", []string{"low", "medium", "high", "xhigh", "max", "ultra"}, false,
		),
		providerModel(
			"gpt-5.6-luna", "GPT-5.6-Luna",
			"Fast and affordable agentic coding model.",
			"medium", []string{"low", "medium", "high", "xhigh", "max"}, false,
		),
		providerModel(
			"gpt-5.5", "GPT-5.5",
			"Frontier model for complex coding, research, and real-world work.",
			"medium", []string{"low", "medium", "high", "xhigh"}, false,
		),
		{
			ID: "gpt-5.2", Model: "gpt-5.2", DisplayName: "GPT-5.2",
			Description:               "Optimized for professional work and long-running agents.",
			SupportedReasoningEfforts: providerGPT52ReasoningEfforts(),
			DefaultReasoningEffort:    "medium",
			InputModalities:           []string{"text", "image"},
			AdditionalSpeedTiers:      []string{},
			ServiceTiers:              []providerDesktopServiceTier{},
		},
	}
	models[0].AvailabilityNux = map[string]string{"message": "Our most capable model yet. " +
		"GPT-5.6 Sol can tackle complex code changes, dig into research, produce polished " +
		"documents, and take on your most ambitious work. Sol is highly capable at lower " +
		"reasoning efforts—try starting lower, then turn it up for harder jobs."}
	models[3].AvailabilityNux = map[string]string{"message": "GPT-5.5 is now available in Codex. " +
		"It's our strongest agentic coding model yet, built to reason through large codebases, " +
		"check assumptions with tools, and keep going until the work is done.\n\n" +
		"Learn more: https://openai.com/index/introducing-gpt-5-5/\n\n"}
	return json.Marshal(struct {
		Data       []providerDesktopModel `json:"data"`
		NextCursor any                    `json:"nextCursor"`
	}{Data: models})
}

func providerModel(id, displayName, description, defaultEffort string,
	efforts []string, isDefault bool,
) providerDesktopModel {
	return providerDesktopModel{
		ID: id, Model: id, DisplayName: displayName, Description: description,
		SupportedReasoningEfforts: providerReasoningEfforts(efforts),
		DefaultReasoningEffort:    defaultEffort,
		InputModalities:           []string{"text", "image"},
		AdditionalSpeedTiers:      []string{"fast"},
		ServiceTiers: []providerDesktopServiceTier{{
			ID: "priority", Name: "Fast", Description: "1.5x speed, increased usage",
		}},
		SupportsPersonality: id == "gpt-5.5",
		IsDefault:           isDefault,
	}
}

func providerReasoningEfforts(values []string) []providerDesktopReasoningEffort {
	descriptions := map[string]string{
		"low":    "Fast responses with lighter reasoning",
		"medium": "Balances speed and reasoning depth for everyday tasks",
		"high":   "Greater reasoning depth for complex problems",
		"xhigh":  "Extra high reasoning depth for complex problems",
		"max":    "Maximum reasoning depth for the hardest problems",
		"ultra":  "Maximum reasoning with automatic task delegation",
	}
	result := make([]providerDesktopReasoningEffort, 0, len(values))
	for _, effort := range values {
		result = append(result, providerDesktopReasoningEffort{
			ReasoningEffort: effort, Description: descriptions[effort],
		})
	}
	return result
}

func providerGPT52ReasoningEfforts() []providerDesktopReasoningEffort {
	return []providerDesktopReasoningEffort{
		{ReasoningEffort: "low", Description: "Balances speed with some reasoning; " +
			"useful for straightforward queries and short explanations"},
		{ReasoningEffort: "medium", Description: "Provides a solid balance of reasoning depth " +
			"and latency for general-purpose tasks"},
		{ReasoningEffort: "high", Description: "Maximizes reasoning depth for complex " +
			"or ambiguous problems"},
		{ReasoningEffort: "xhigh", Description: "Extra high reasoning for complex problems"},
	}
}
