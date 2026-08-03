package codexsettings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

type Preferences struct {
	Model           *string `json:"model"`
	ReasoningEffort *string `json:"reasoningEffort"`
	ServiceTier     *string `json:"serviceTier"`
}

type EffectivePreferences struct {
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoningEffort"`
	ServiceTier     string `json:"serviceTier"`
}

type RepositorySettings struct {
	ID        uuid.UUID            `json:"id"`
	Owner     string               `json:"owner"`
	Name      string               `json:"name"`
	Settings  Preferences          `json:"settings"`
	Effective EffectivePreferences `json:"effective"`
}

type Service struct{ db *sql.DB }

func NewService(db *sql.DB) *Service { return &Service{db: db} }

func (s *Service) List(ctx context.Context) ([]RepositorySettings, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, owner, name FROM repositories WHERE enabled = true ORDER BY owner, name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make([]RepositorySettings, 0)
	for rows.Next() {
		var item RepositorySettings
		if err := rows.Scan(&item.ID, &item.Owner, &item.Name); err != nil {
			return nil, err
		}
		item.Settings, err = s.load(ctx, item.ID)
		if err != nil {
			return nil, err
		}
		item.Effective, err = s.Resolve(ctx, item.ID, uuid.Nil)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Service) Save(ctx context.Context, id uuid.UUID, value Preferences) error {
	if err := validate(value); err != nil {
		return err
	}
	var exists bool
	query := `SELECT EXISTS(SELECT 1 FROM repositories WHERE id = $1)`
	if err := s.db.QueryRowContext(ctx, query, id).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return sql.ErrNoRows
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO github_agent_repository_overrides
		(repository_id, model, reasoning_effort, service_tier)
		VALUES ($1,$2,$3,$4) ON CONFLICT(repository_id) DO UPDATE SET
		model = EXCLUDED.model, reasoning_effort = EXCLUDED.reasoning_effort,
		service_tier = EXCLUDED.service_tier, updated_at = now()`, id,
		nullable(value.Model), nullable(value.ReasoningEffort), nullable(value.ServiceTier))
	return err
}

func (s *Service) Resolve(ctx context.Context, repositoryID, profileID uuid.UUID) (EffectivePreferences, error) {
	result := EffectivePreferences{ServiceTier: "standard"}
	profile, err := s.profileDefaults(ctx, profileID)
	if err != nil {
		return result, err
	}
	apply(&result, profile)
	if repositoryID != uuid.Nil {
		value, loadErr := s.load(ctx, repositoryID)
		if loadErr != nil {
			return result, loadErr
		}
		apply(&result, value)
	}
	return result, nil
}

func (s *Service) load(ctx context.Context, id uuid.UUID) (Preferences, error) {
	var model, effort, tier sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT model, reasoning_effort, service_tier
		FROM github_agent_repository_overrides WHERE repository_id = $1`, id).
		Scan(&model, &effort, &tier)
	if errors.Is(err, sql.ErrNoRows) {
		return Preferences{}, nil
	}
	if err != nil {
		return Preferences{}, err
	}
	return Preferences{Model: pointer(model), ReasoningEffort: pointer(effort), ServiceTier: pointer(tier)}, nil
}

func (s *Service) profileDefaults(ctx context.Context, profileID uuid.UUID) (Preferences, error) {
	var model, effort, tier sql.NullString
	query := `SELECT model, reasoning_effort, service_tier FROM agent_profiles ORDER BY created_at LIMIT 1`
	args := []any{}
	if profileID != uuid.Nil {
		query = `SELECT model, reasoning_effort, service_tier FROM agent_profiles WHERE id = $1`
		args = append(args, profileID)
	}
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&model, &effort, &tier)
	if errors.Is(err, sql.ErrNoRows) {
		return Preferences{}, nil
	}
	if err != nil {
		return Preferences{}, err
	}
	return Preferences{Model: pointer(model), ReasoningEffort: normalizedEffort(effort), ServiceTier: normalizedTier(tier)}, nil
}

func RuntimeServiceTier(value string) string {
	canonical, ok := CanonicalServiceTier(value)
	if ok && canonical == "fast" {
		return "fast"
	}
	return ""
}

func CanonicalServiceTier(value string) (string, bool) {
	switch strings.TrimSpace(value) {
	case "":
		return "", true
	case "default", "standard":
		return "standard", true
	case "fast", "priority":
		return "fast", true
	default:
		return "", false
	}
}

func AppliedServiceTier(value string) (string, bool) {
	canonical, ok := CanonicalServiceTier(value)
	if !ok {
		return "", false
	}
	if canonical == "" {
		return "", true
	}
	if canonical == "fast" {
		return "priority", true
	}
	return "default", true
}

func ValidatePreferences(value Preferences) error {
	return validate(value)
}

func apply(target *EffectivePreferences, value Preferences) {
	if value.Model != nil {
		target.Model = strings.TrimSpace(*value.Model)
	}
	if value.ReasoningEffort != nil {
		target.ReasoningEffort = strings.TrimSpace(*value.ReasoningEffort)
	}
	if value.ServiceTier != nil {
		target.ServiceTier = strings.TrimSpace(*value.ServiceTier)
	}
}

func validate(value Preferences) error {
	if value.Model != nil {
		text := strings.TrimSpace(*value.Model)
		if text == "" || len(text) > 128 {
			return errors.New("模型名称长度必须为 1-128")
		}
		*value.Model = text
	}
	if value.ReasoningEffort != nil {
		if !ValidReasoningEffort(*value.ReasoningEffort) {
			return fmt.Errorf("不支持的思考等级 %q", *value.ReasoningEffort)
		}
	}
	if value.ServiceTier != nil && *value.ServiceTier != "standard" && *value.ServiceTier != "fast" {
		return fmt.Errorf("不支持的服务等级 %q", *value.ServiceTier)
	}
	return nil
}

func pointer(value sql.NullString) *string {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil
	}
	text := strings.TrimSpace(value.String)
	return &text
}

func normalizedEffort(value sql.NullString) *string {
	result := pointer(value)
	if result == nil {
		return nil
	}
	if !ValidReasoningEffort(*result) {
		return nil
	}
	return result
}

func normalizedTier(value sql.NullString) *string {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil
	}
	result := "standard"
	if strings.TrimSpace(value.String) == "fast" {
		result = "fast"
	}
	return &result
}

func nullable(value *string) any {
	if value == nil {
		return nil
	}
	return strings.TrimSpace(*value)
}
