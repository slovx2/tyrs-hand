package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexsettings"
)

const clientProtocolVersion = 3
const clientDeviceContext = "clientDeviceID"

type clientSessionSettings struct {
	AgentProfileID    uuid.UUID `json:"agentProfileId"`
	Model             *string   `json:"model"`
	ReasoningEffort   *string   `json:"reasoningEffort"`
	ServiceTier       string    `json:"serviceTier"`
	CollaborationMode string    `json:"collaborationMode"`
	SettingsVersion   int64     `json:"settingsVersion"`
}

type clientLastSettings struct {
	AgentProfileID    uuid.UUID `json:"agentProfileId"`
	Model             *string   `json:"model"`
	ReasoningEffort   *string   `json:"reasoningEffort"`
	ServiceTier       string    `json:"serviceTier"`
	CollaborationMode string    `json:"collaborationMode"`
	Version           int64     `json:"settingsVersion"`
}

func (s *Server) clientControlInstanceID(ctx context.Context) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.db.QueryRowContext(ctx, `SELECT id FROM control_instances WHERE singleton=true`).Scan(&id)
	return id, err
}

func loadClientLastSettings(ctx context.Context, db *sql.DB, administratorID uuid.UUID,
) (*clientLastSettings, error) {
	var result clientLastSettings
	var model, effort sql.NullString
	err := db.QueryRowContext(ctx, `SELECT agent_profile_id,model,reasoning_effort,
		service_tier,collaboration_mode,version FROM client_user_preferences
		WHERE administrator_id=$1`, administratorID).Scan(&result.AgentProfileID, &model,
		&effort, &result.ServiceTier, &result.CollaborationMode, &result.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result.Model = nullableString(model)
	result.ReasoningEffort = nullableString(effort)
	return &result, nil
}

func nullableString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	text := value.String
	return &text
}

func validateClientSettings(model, effort *string, tier, mode string) error {
	modelValue := strings.TrimSpace(stringValue(model))
	effortValue := strings.TrimSpace(stringValue(effort))
	if tier != "standard" && tier != "fast" {
		return errors.New("服务等级无效")
	}
	if mode != "default" && mode != "plan" {
		return errors.New("协作模式无效")
	}
	if modelValue != "" && len(modelValue) > 128 {
		return errors.New("模型名称过长")
	}
	if !codexsettings.ValidReasoningEffort(effortValue) {
		return errors.New("思考等级无效")
	}
	return nil
}

func insertClientUpdate(ctx context.Context, tx *sql.Tx, sessionID *uuid.UUID,
	updateType, entityType, entityID string, entitySeq, entityVersion *int64, payload any,
) (int64, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	var cursor int64
	err = tx.QueryRowContext(ctx, `INSERT INTO client_updates(
		session_id,update_type,entity_type,entity_id,entity_seq,entity_version,payload,durable)
		VALUES ($1,$2,NULLIF($3,''),$4,$5,$6,$7,true) RETURNING cursor`,
		nullableUUID(sessionID), updateType, entityType, entityID, entitySeq, entityVersion,
		encoded).Scan(&cursor)
	return cursor, err
}

func nullableUUID(value *uuid.UUID) any {
	if value == nil || *value == uuid.Nil {
		return nil
	}
	return *value
}

func fallbackSessionTitle(text string) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if text == "" {
		return "新的开发任务"
	}
	runes := []rune(text)
	if len(runes) <= 60 {
		return text
	}
	return string(runes[:59]) + "…"
}

func clientSyncRetentionStart() time.Time {
	return time.Now().UTC().Add(-30 * 24 * time.Hour)
}
