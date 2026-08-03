package codexcatalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

type Caller interface {
	Call(context.Context, string, any, any) error
}

type ReasoningEffort struct {
	ReasoningEffort string `json:"reasoningEffort"`
	Description     string `json:"description"`
}

type ServiceTier struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type Model struct {
	ID                        string            `json:"id"`
	Model                     string            `json:"model"`
	DisplayName               string            `json:"displayName"`
	Description               string            `json:"description"`
	SupportedReasoningEfforts []ReasoningEffort `json:"supportedReasoningEfforts"`
	DefaultReasoningEffort    string            `json:"defaultReasoningEffort"`
	InputModalities           []string          `json:"inputModalities,omitempty"`
	AdditionalSpeedTiers      []string          `json:"additionalSpeedTiers,omitempty"`
	ServiceTiers              []ServiceTier     `json:"serviceTiers,omitempty"`
	DefaultServiceTier        *string           `json:"defaultServiceTier"`
	IsDefault                 bool              `json:"isDefault"`
	Hidden                    bool              `json:"hidden"`
}

type Catalog struct {
	Data       []Model `json:"data"`
	NextCursor *string `json:"nextCursor"`
}

func Fetch(ctx context.Context, caller Caller) (json.RawMessage, error) {
	models := make([]json.RawMessage, 0)
	var cursor *string
	for {
		var page struct {
			Data       []json.RawMessage `json:"data"`
			NextCursor *string           `json:"nextCursor"`
		}
		params := map[string]any{"limit": 100}
		if cursor != nil {
			params["cursor"] = *cursor
		}
		if err := caller.Call(ctx, "model/list", params, &page); err != nil {
			return nil, err
		}
		models = append(models, page.Data...)
		if page.NextCursor == nil || strings.TrimSpace(*page.NextCursor) == "" {
			break
		}
		cursor = page.NextCursor
	}
	return json.Marshal(struct {
		Data       []json.RawMessage `json:"data"`
		NextCursor any               `json:"nextCursor"`
	}{Data: models})
}

func Parse(raw json.RawMessage) (Catalog, error) {
	var catalog Catalog
	if len(raw) == 0 {
		return catalog, errors.New("codex 模型目录为空")
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return catalog, fmt.Errorf("解析 Codex 模型目录: %w", err)
	}
	if catalog.Data == nil {
		return catalog, errors.New("codex 模型目录缺少 data")
	}
	return catalog, nil
}

func ModelIDs(catalogs map[uuid.UUID]json.RawMessage) []string {
	result := make([]string, 0)
	for _, raw := range catalogs {
		catalog, err := Parse(raw)
		if err != nil {
			continue
		}
		for _, model := range catalog.Data {
			id := strings.TrimSpace(model.ID)
			if id != "" && !model.Hidden && !slices.Contains(result, id) {
				result = append(result, id)
			}
		}
	}
	slices.Sort(result)
	return result
}

func ReasoningEfforts(catalogs map[uuid.UUID]json.RawMessage) []string {
	result := make([]string, 0)
	for _, raw := range catalogs {
		catalog, err := Parse(raw)
		if err != nil {
			continue
		}
		for _, model := range catalog.Data {
			if model.Hidden {
				continue
			}
			for _, option := range model.SupportedReasoningEfforts {
				effort := strings.TrimSpace(option.ReasoningEffort)
				if effort != "" && !slices.Contains(result, effort) {
					result = append(result, effort)
				}
			}
		}
	}
	return result
}

func EnvironmentCatalogs(ctx context.Context, db *sql.DB,
	environmentIDs []uuid.UUID,
) (map[uuid.UUID]json.RawMessage, error) {
	result := make(map[uuid.UUID]json.RawMessage)
	if len(environmentIDs) == 0 {
		return result, nil
	}
	ids := make([]string, 0, len(environmentIDs))
	for _, environmentID := range environmentIDs {
		ids = append(ids, environmentID.String())
	}
	rows, err := db.QueryContext(ctx, `SELECT environment.id,
		node.metadata->'modelCatalogs'->environment.id::text
		FROM discord_development_environments environment
		JOIN execution_nodes node ON node.id=environment.execution_node_id
		WHERE environment.id = ANY($1::uuid[]) AND node.enabled AND node.status='online'
			AND node.heartbeat_at > now() - interval '2 minutes'
			AND node.metadata->'modelCatalogs'->environment.id::text IS NOT NULL`, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var environmentID uuid.UUID
		var raw []byte
		if err := rows.Scan(&environmentID, &raw); err != nil {
			return nil, err
		}
		if _, err := Parse(raw); err == nil {
			result[environmentID] = append(json.RawMessage(nil), raw...)
		}
	}
	return result, rows.Err()
}

func OnlineCatalogs(ctx context.Context, db *sql.DB) (map[uuid.UUID]json.RawMessage, error) {
	rows, err := db.QueryContext(ctx, `SELECT metadata->'modelCatalogs' FROM execution_nodes
		WHERE enabled AND status='online' AND heartbeat_at > now() - interval '2 minutes'
			AND metadata ? 'modelCatalogs'`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make(map[uuid.UUID]json.RawMessage)
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var catalogs map[string]json.RawMessage
		if json.Unmarshal(raw, &catalogs) != nil {
			continue
		}
		for key, catalog := range catalogs {
			environmentID, err := uuid.Parse(key)
			if err != nil {
				continue
			}
			if _, err := Parse(catalog); err == nil {
				result[environmentID] = append(json.RawMessage(nil), catalog...)
			}
		}
	}
	return result, rows.Err()
}
