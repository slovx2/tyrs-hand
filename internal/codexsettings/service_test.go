package codexsettings

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

func TestRuntimeServiceTier(t *testing.T) {
	if got := RuntimeServiceTier("fast"); got != "fast" {
		t.Fatalf("fast = %q", got)
	}
	if got := RuntimeServiceTier("standard"); got != "" {
		t.Fatalf("standard 应省略，得到 %q", got)
	}
	if got := RuntimeServiceTier("priority"); got != "fast" {
		t.Fatalf("priority 应归一化为 fast，得到 %q", got)
	}
}

func TestReasoningEffortFollowsCodexOpaqueValue(t *testing.T) {
	if !ValidReasoningEffort("future-effort") {
		t.Fatal("Codex 新增的推理档位不应被本地枚举拒绝")
	}
	if ValidReasoningEffort(strings.Repeat("x", 65)) {
		t.Fatal("推理档位仍需遵守协议长度边界")
	}
}

func TestServiceTierBoundaries(t *testing.T) {
	tests := []struct {
		input     string
		canonical string
		applied   string
	}{
		{input: "", canonical: "", applied: ""},
		{input: "standard", canonical: "standard", applied: "default"},
		{input: "default", canonical: "standard", applied: "default"},
		{input: "fast", canonical: "fast", applied: "priority"},
		{input: "priority", canonical: "fast", applied: "priority"},
	}
	for _, item := range tests {
		canonical, ok := CanonicalServiceTier(item.input)
		if !ok || canonical != item.canonical {
			t.Fatalf("CanonicalServiceTier(%q) = %q, %v", item.input, canonical, ok)
		}
		applied, ok := AppliedServiceTier(item.input)
		if !ok || applied != item.applied {
			t.Fatalf("AppliedServiceTier(%q) = %q, %v", item.input, applied, ok)
		}
	}
	if _, ok := CanonicalServiceTier("flex"); ok {
		t.Fatal("未声明的 flex 不应进入产品配置")
	}
}

func TestPreferenceLayersOverrideOnlyExplicitValues(t *testing.T) {
	modelProvider, modelRepository := "profile-model", "repository-model"
	effortProfile := "medium"
	tierProvider, tierRepository := "standard", "fast"
	result := EffectivePreferences{ServiceTier: "standard"}
	apply(&result, Preferences{Model: &modelProvider, ServiceTier: &tierProvider})
	apply(&result, Preferences{ReasoningEffort: &effortProfile})
	apply(&result, Preferences{Model: &modelRepository, ServiceTier: &tierRepository})

	if result.Model != modelRepository || result.ReasoningEffort != effortProfile || result.ServiceTier != tierRepository {
		t.Fatalf("分层覆盖结果不正确: %+v", result)
	}
}

func TestListSerializesEmptyCollectionsAsArrays(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	repositoryID := uuid.New()
	mock.ExpectQuery("SELECT id, owner, name FROM repositories").
		WillReturnRows(sqlmock.NewRows([]string{"id", "owner", "name"}).
			AddRow(repositoryID, "example", "repository"))
	mock.ExpectQuery("SELECT model, reasoning_effort, service_tier").
		WithArgs(repositoryID).WillReturnRows(sqlmock.NewRows(nil))
	mock.ExpectQuery("FROM agent_profiles").WillReturnRows(sqlmock.NewRows(nil))
	mock.ExpectQuery("SELECT model, reasoning_effort, service_tier").
		WithArgs(repositoryID).WillReturnRows(sqlmock.NewRows(nil))

	items, err := NewService(db).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"settings":`) {
		t.Fatalf("Repository 设置未序列化: %s", encoded)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
