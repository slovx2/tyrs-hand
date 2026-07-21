package codexsettings

import "testing"

func TestRuntimeServiceTier(t *testing.T) {
	if got := RuntimeServiceTier("fast"); got != "fast" {
		t.Fatalf("fast = %q", got)
	}
	if got := RuntimeServiceTier("standard"); got != "" {
		t.Fatalf("standard 应省略，得到 %q", got)
	}
}

func TestPreferenceLayersOverrideOnlyExplicitValues(t *testing.T) {
	modelProvider, modelRepository := "provider-model", "repository-model"
	effortProfile, effortForum := "medium", "xhigh"
	tierProvider, tierRepository := "standard", "fast"
	result := EffectivePreferences{ServiceTier: "standard"}
	apply(&result, Preferences{Model: &modelProvider, ServiceTier: &tierProvider})
	apply(&result, Preferences{ReasoningEffort: &effortProfile})
	apply(&result, Preferences{Model: &modelRepository, ServiceTier: &tierRepository})
	apply(&result, Preferences{ReasoningEffort: &effortForum})

	if result.Model != modelRepository || result.ReasoningEffort != effortForum || result.ServiceTier != tierRepository {
		t.Fatalf("分层覆盖结果不正确: %+v", result)
	}
}
