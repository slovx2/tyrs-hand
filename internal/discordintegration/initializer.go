package discordintegration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/google/uuid"
)

const (
	InitializationIncremental = "incremental"
	InitializationFresh       = "fresh"
)

type InitializationAction struct {
	Kind         string      `json:"kind"`
	ResourceID   string      `json:"resourceId,omitempty"`
	Spec         ChannelSpec `json:"spec,omitempty"`
	OwnerUserID  string      `json:"ownerUserId,omitempty"`
	RepositoryID string      `json:"repositoryId,omitempty"`
}

type InitializationPlan struct {
	Preflight PreflightResult        `json:"preflight"`
	Actions   []InitializationAction `json:"actions"`
}

func BaseChannelSpecs() []ChannelSpec {
	return []ChannelSpec{
		{Key: "category.system", Name: "系统", Kind: "category"},
		{Key: "system.rules", ParentKey: "category.system", Name: "规则", Kind: "text", Topic: "Tyrs Hand 私有服务规则"},
		{Key: "system.updates", ParentKey: "category.system", Name: "社区更新", Kind: "text", Topic: "Discord Community 必需频道"},
		{Key: "system.status", ParentKey: "category.system", Name: "系统状态", Kind: "text", Topic: "Tyrs Hand 系统状态，每分钟更新"},
		{Key: "system.alerts", ParentKey: "category.system", Name: "系统告警", Kind: "text", Topic: "仅基础设施异常"},
		{Key: "category.github.01", Name: "GitHub 任务 01", Kind: "category"},
		{Key: "category.codex.01", Name: "Codex 会话 01", Kind: "category"},
	}
}

func BuildInitializationPlan(mode string, guild RemoteGuild, managed []ManagedResource, desired []ChannelSpec) (InitializationPlan, error) {
	if mode != InitializationIncremental && mode != InitializationFresh {
		return InitializationPlan{}, errors.New("初始化模式必须是 incremental 或 fresh")
	}
	result := InitializationPlan{Preflight: PreflightResult{
		GuildID: guild.ID, Mode: mode, Channels: len(guild.Channels), Safe: true,
		Creates: []string{}, Updates: []string{}, Deletes: []string{},
		Conflicts: []Conflict{}, MissingAccess: []string{},
	}}
	if mode == InitializationFresh {
		if guild.CommunityEnabled {
			result.Actions = append(result.Actions, InitializationAction{Kind: "community.disable"})
		}
		channels := slices.Clone(guild.Channels)
		sort.SliceStable(channels, func(i, j int) bool {
			return channels[i].Kind != "category" && channels[j].Kind == "category"
		})
		for _, channel := range channels {
			result.Preflight.Deletes = append(result.Preflight.Deletes, channel.Name)
			result.Actions = append(result.Actions, InitializationAction{Kind: "channel.delete", ResourceID: channel.ID})
		}
		result.Actions = append(result.Actions, InitializationAction{Kind: "projection.reset"})
		appendCreates(&result, desired)
		result.Actions = append(result.Actions, InitializationAction{Kind: "community.enable"})
		return result, nil
	}

	remoteByID := make(map[string]RemoteChannel, len(guild.Channels))
	remoteByName := make(map[string][]RemoteChannel, len(guild.Channels))
	for _, channel := range guild.Channels {
		remoteByID[channel.ID] = channel
		remoteByName[channel.Name] = append(remoteByName[channel.Name], channel)
	}
	managedByKey := make(map[string]ManagedResource, len(managed))
	managedIDs := make(map[string]bool, len(managed))
	for _, resource := range managed {
		managedByKey[resource.Key] = resource
		managedIDs[resource.DiscordID] = true
	}
	for _, spec := range orderedSpecs(desired) {
		resource, exists := managedByKey[spec.Key]
		if !exists {
			for _, sameName := range remoteByName[spec.Name] {
				if !managedIDs[sameName.ID] {
					result.Preflight.Conflicts = append(result.Preflight.Conflicts, Conflict{
						Name: spec.Name, Reason: "存在未受 Tyrs Hand 管理的同名 Channel",
					})
				}
			}
			result.Preflight.Creates = append(result.Preflight.Creates, spec.Name)
			result.Actions = append(result.Actions, InitializationAction{Kind: "channel.create", Spec: spec})
			continue
		}
		remote, found := remoteByID[resource.DiscordID]
		if !found {
			result.Preflight.Creates = append(result.Preflight.Creates, spec.Name)
			result.Actions = append(result.Actions, InitializationAction{Kind: "channel.create", Spec: spec})
			continue
		}
		if remote.Kind != spec.Kind {
			result.Preflight.Conflicts = append(result.Preflight.Conflicts, Conflict{
				Name: spec.Name, Reason: fmt.Sprintf("受管资源类型为 %s，预期为 %s", remote.Kind, spec.Kind),
			})
			continue
		}
		parentID := ""
		if parent, ok := managedByKey[spec.ParentKey]; ok {
			parentID = parent.DiscordID
		}
		topic := managedTopic(spec.Topic, managedMarker(spec.Key))
		if remote.Name != spec.Name || remote.ParentID != parentID || (spec.Kind != "category" && remote.Topic != topic) {
			spec.ParentKey = parentID
			result.Preflight.Updates = append(result.Preflight.Updates, spec.Name)
			result.Actions = append(result.Actions, InitializationAction{Kind: "channel.update", ResourceID: remote.ID, Spec: spec})
		}
	}
	if !guild.CommunityEnabled {
		result.Actions = append(result.Actions, InitializationAction{Kind: "community.enable"})
	}
	result.Preflight.Safe = len(result.Preflight.Conflicts) == 0 && len(result.Preflight.MissingAccess) == 0
	return result, nil
}

func ValidateFreshConfirmation(guildID, confirmation string) error {
	expected := "DELETE ALL CHANNELS " + guildID
	if confirmation != expected {
		return fmt.Errorf("全新初始化确认指令必须精确输入 %q", expected)
	}
	return nil
}

func (m *Manager) ManagedResources(ctx context.Context, guildID string) ([]ManagedResource, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT resource_key, discord_id, COALESCE(parent_discord_id, ''),
		name, kind, managed_marker FROM discord_resources WHERE guild_id = $1 AND status = 'active'`, guildID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []ManagedResource
	for rows.Next() {
		var resource ManagedResource
		if err := rows.Scan(&resource.Key, &resource.DiscordID, &resource.ParentID,
			&resource.Name, &resource.Kind, &resource.Marker); err != nil {
			return nil, err
		}
		result = append(result, resource)
	}
	return result, rows.Err()
}

func (m *Manager) CreateInitialization(ctx context.Context, administratorID uuid.UUID, plan InitializationPlan, confirmation string) (uuid.UUID, error) {
	if !plan.Preflight.Safe {
		return uuid.Nil, errors.New("初始化预检存在冲突，未创建任何操作")
	}
	if plan.Preflight.Mode == InitializationFresh {
		if err := ValidateFreshConfirmation(plan.Preflight.GuildID, confirmation); err != nil {
			return uuid.Nil, err
		}
	}
	encoded, err := json.Marshal(plan.Preflight)
	if err != nil {
		return uuid.Nil, err
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return uuid.Nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var operationID uuid.UUID
	err = tx.QueryRowContext(ctx, `INSERT INTO discord_initialization_operations
		(guild_id, mode, requested_by, preflight, confirmation) VALUES ($1, $2, $3, $4, NULLIF($5, '')) RETURNING id`,
		plan.Preflight.GuildID, plan.Preflight.Mode, administratorID, encoded, confirmation).Scan(&operationID)
	if err != nil {
		return uuid.Nil, err
	}
	for index, action := range plan.Actions {
		request, marshalErr := json.Marshal(action)
		if marshalErr != nil {
			return uuid.Nil, marshalErr
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO discord_initialization_steps(operation_id, step_key, ordinal, request)
			VALUES ($1, $2, $3, $4)`, operationID, fmt.Sprintf("%03d-%s", index+1, action.Kind), index+1, request)
		if err != nil {
			return uuid.Nil, err
		}
	}
	return operationID, tx.Commit()
}

func (m *Manager) Operation(ctx context.Context, id uuid.UUID) (Operation, error) {
	var result Operation
	var raw []byte
	err := m.db.QueryRowContext(ctx, `SELECT id::text, guild_id, mode, status, preflight, COALESCE(error, ''), created_at, updated_at
		FROM discord_initialization_operations WHERE id = $1`, id).
		Scan(&result.ID, &result.GuildID, &result.Mode, &result.Status, &raw, &result.Error, &result.CreatedAt, &result.UpdatedAt)
	if err != nil {
		return Operation{}, err
	}
	return result, json.Unmarshal(raw, &result.Preflight)
}

func appendCreates(result *InitializationPlan, desired []ChannelSpec) {
	for _, spec := range orderedSpecs(desired) {
		result.Preflight.Creates = append(result.Preflight.Creates, spec.Name)
		result.Actions = append(result.Actions, InitializationAction{Kind: "channel.create", Spec: spec})
	}
}

func orderedSpecs(specs []ChannelSpec) []ChannelSpec {
	result := slices.Clone(specs)
	sort.SliceStable(result, func(i, j int) bool { return result[i].Kind == "category" && result[j].Kind != "category" })
	return result
}

func managedMarker(key string) string { return "[tyrs-hand:" + strings.ToLower(key) + "]" }
