package worker

import (
	"encoding/json"

	"github.com/slovx2/tyrs-hand/internal/codexsettings"
)

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
	DisplayName               string                           `json:"displayName"`
	Description               string                           `json:"description"`
	SupportedReasoningEfforts []providerDesktopReasoningEffort `json:"supportedReasoningEfforts"`
	DefaultReasoningEffort    string                           `json:"defaultReasoningEffort"`
	InputModalities           []string                         `json:"inputModalities"`
	AdditionalSpeedTiers      []string                         `json:"additionalSpeedTiers"`
	ServiceTiers              []providerDesktopServiceTier     `json:"serviceTiers"`
	DefaultServiceTier        any                              `json:"defaultServiceTier"`
	IsDefault                 bool                             `json:"isDefault"`
	Upgrade                   any                              `json:"upgrade"`
	UpgradeInfo               any                              `json:"upgradeInfo"`
	AvailabilityNux           any                              `json:"availabilityNux"`
	Hidden                    bool                             `json:"hidden"`
	SupportsPersonality       bool                             `json:"supportsPersonality"`
}

// providerDesktopModelCatalog 与客户端参数选择器共用 codexsettings 的模型目录。
func providerDesktopModelCatalog() (json.RawMessage, error) {
	models := make([]providerDesktopModel, 0, len(codexsettings.ModelCatalog()))
	for _, option := range codexsettings.ModelCatalog() {
		model := providerDesktopModel{ID: option.ID, Model: option.ID,
			DisplayName: option.DisplayName, Description: option.Description,
			DefaultReasoningEffort: option.DefaultReasoningEffort,
			InputModalities:        option.InputModalities, IsDefault: option.Default,
			SupportsPersonality: option.ID == "gpt-5.5"}
		for _, effort := range option.SupportedReasoningEfforts {
			model.SupportedReasoningEfforts = append(model.SupportedReasoningEfforts,
				providerDesktopReasoningEffort{ReasoningEffort: effort.ID,
					Description: effort.Description})
		}
		for _, tier := range option.ServiceTiers {
			model.AdditionalSpeedTiers = append(model.AdditionalSpeedTiers, tier.ID)
			model.ServiceTiers = append(model.ServiceTiers, providerDesktopServiceTier{
				ID: "priority", Name: "Fast", Description: tier.Description})
		}
		models = append(models, model)
	}
	return json.Marshal(struct {
		Data       []providerDesktopModel `json:"data"`
		NextCursor any                    `json:"nextCursor"`
	}{Data: models})
}
