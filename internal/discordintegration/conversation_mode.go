package discordintegration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/codexsettings"
)

type ConversationModeState struct {
	ConversationID   uuid.UUID
	Mode             string
	Revision         int64
	TriggerMode      string
	TriggerRevision  int64
	Model            string
	ReasoningEffort  string
	ServiceTier      string
	SettingsRevision int64
	Awaiting         bool
	Busy             bool
}

type ConfigurationChange struct {
	Field  string
	Before string
	After  string
}

type ConfigurationUpdate struct {
	State   ConversationModeState
	Stale   bool
	Changes []ConfigurationChange
}

func (s *ConversationService) ConversationMode(ctx context.Context, guildID, threadID,
	userID string,
) (ConversationModeState, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ConversationModeState{}, err
	}
	defer func() { _ = tx.Rollback() }()
	state, _, err := s.conversationModeState(ctx, tx, guildID, threadID, userID, false)
	if err != nil {
		return ConversationModeState{}, err
	}
	return state, tx.Commit()
}

func (s *ConversationService) SetConversationMode(ctx context.Context, guildID, threadID,
	userID string, expectedConversationID uuid.UUID, expectedRevision int64, target string,
) (ConfigurationUpdate, error) {
	if target != "default" && target != "plan" {
		return ConfigurationUpdate{}, errors.New("目标模式无效")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ConfigurationUpdate{}, err
	}
	defer func() { _ = tx.Rollback() }()
	state, controlID, err := s.conversationModeState(ctx, tx, guildID, threadID, userID, true)
	if err != nil {
		return ConfigurationUpdate{}, err
	}
	if state.ConversationID != expectedConversationID {
		return ConfigurationUpdate{}, errors.New("这个模式按钮不属于当前 Codex 会话")
	}
	if state.SettingsRevision != expectedRevision {
		return ConfigurationUpdate{State: state, Stale: true}, tx.Commit()
	}
	update := ConfigurationUpdate{State: state}
	if state.Mode != target {
		update.Changes = append(update.Changes, ConfigurationChange{
			Field: "collaboration_mode", Before: state.Mode, After: target})
		state.Mode = target
		state.Revision++
		state.SettingsRevision++
		_, err = tx.ExecContext(ctx, `UPDATE discord_conversations SET collaboration_mode = $2,
			collaboration_mode_revision = $3, settings_revision = $4,
			updated_at = now() WHERE id = $1`, state.ConversationID, state.Mode,
			state.Revision, state.SettingsRevision)
		if err == nil && controlID != uuid.Nil {
			_, err = tx.ExecContext(ctx, `UPDATE codex_thread_controls SET collaboration_mode = $2,
				collaboration_mode_revision = $3, settings_revision = $4,
				updated_at = now() WHERE id = $1`, controlID, state.Mode,
				state.Revision, state.SettingsRevision)
		}
		if err != nil {
			return ConfigurationUpdate{}, err
		}
	}
	update.State = state
	if err := refreshWaitingConfigurationProjectionTx(ctx, tx, state); err != nil {
		return ConfigurationUpdate{}, err
	}
	return update, tx.Commit()
}

func (s *ConversationService) SetTriggerMode(ctx context.Context, guildID, threadID,
	userID string, expectedConversationID uuid.UUID, expectedRevision int64, target string,
) (ConfigurationUpdate, error) {
	if target != "interactive" && target != "discussion" {
		return ConfigurationUpdate{}, errors.New("目标消息触发模式无效")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ConfigurationUpdate{}, err
	}
	defer func() { _ = tx.Rollback() }()
	state, controlID, err := s.conversationModeState(ctx, tx, guildID, threadID, userID, true)
	if err != nil {
		return ConfigurationUpdate{}, err
	}
	if state.ConversationID != expectedConversationID {
		return ConfigurationUpdate{}, errors.New("这个模式按钮不属于当前 Codex 会话")
	}
	if state.SettingsRevision != expectedRevision {
		return ConfigurationUpdate{State: state, Stale: true}, tx.Commit()
	}
	update := ConfigurationUpdate{State: state}
	if state.TriggerMode != target {
		update.Changes = append(update.Changes, ConfigurationChange{
			Field: "trigger_mode", Before: state.TriggerMode, After: target})
		state.TriggerMode = target
		state.TriggerRevision++
		state.SettingsRevision++
		_, err = tx.ExecContext(ctx, `UPDATE discord_conversations SET trigger_mode = $2,
			trigger_mode_revision = $3, settings_revision = $4,
			updated_at = now() WHERE id = $1`, state.ConversationID, state.TriggerMode,
			state.TriggerRevision, state.SettingsRevision)
		if err == nil && controlID != uuid.Nil {
			_, err = tx.ExecContext(ctx, `UPDATE codex_thread_controls SET
				settings_revision = $2, updated_at = now() WHERE id = $1`,
				controlID, state.SettingsRevision)
		}
		if err != nil {
			return ConfigurationUpdate{}, err
		}
	}
	update.State = state
	if err := refreshWaitingConfigurationProjectionTx(ctx, tx, state); err != nil {
		return ConfigurationUpdate{}, err
	}
	return update, tx.Commit()
}

func (s *ConversationService) SetRuntimePreferences(ctx context.Context, guildID, threadID,
	userID string, expectedConversationID uuid.UUID, expectedRevision int64,
	selected ConversationConfiguration,
) (ConfigurationUpdate, error) {
	if selected.ServiceTier != "standard" && selected.ServiceTier != "fast" {
		return ConfigurationUpdate{}, errors.New("所选服务等级无效")
	}
	value := codexsettings.Preferences{Model: optionalPreference(selected.Model),
		ReasoningEffort: optionalPreference(selected.ReasoningEffort),
		ServiceTier:     optionalPreference(selected.ServiceTier)}
	if err := codexsettings.ValidatePreferences(value); err != nil {
		return ConfigurationUpdate{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ConfigurationUpdate{}, err
	}
	defer func() { _ = tx.Rollback() }()
	state, controlID, err := s.conversationModeState(ctx, tx, guildID, threadID, userID, true)
	if err != nil {
		return ConfigurationUpdate{}, err
	}
	if state.ConversationID != expectedConversationID {
		return ConfigurationUpdate{}, errors.New("这个设置表单不属于当前 Codex 会话")
	}
	if state.SettingsRevision != expectedRevision {
		return ConfigurationUpdate{State: state, Stale: true}, tx.Commit()
	}
	model := strings.TrimSpace(selected.Model)
	effort := strings.TrimSpace(selected.ReasoningEffort)
	tier := strings.TrimSpace(selected.ServiceTier)
	update := ConfigurationUpdate{State: state}
	for _, item := range []ConfigurationChange{
		{Field: "model", Before: state.Model, After: model},
		{Field: "reasoning_effort", Before: state.ReasoningEffort, After: effort},
		{Field: "service_tier", Before: state.ServiceTier, After: tier},
	} {
		if item.Before != item.After {
			update.Changes = append(update.Changes, item)
		}
	}
	if state.Model != model || state.ReasoningEffort != effort || state.ServiceTier != tier {
		state.Model, state.ReasoningEffort, state.ServiceTier = model, effort, tier
		state.SettingsRevision++
		_, err = tx.ExecContext(ctx, `UPDATE discord_conversations SET model = NULLIF($2,''),
			reasoning_effort = NULLIF($3,''), service_tier = $4,
			settings_revision = $5, updated_at = now() WHERE id = $1`,
			state.ConversationID, model, effort, tier, state.SettingsRevision)
		if err == nil && controlID != uuid.Nil {
			_, err = tx.ExecContext(ctx, `UPDATE codex_thread_controls SET model = NULLIF($2,''),
				reasoning_effort = NULLIF($3,''), service_tier = $4,
				settings_revision = $5, runtime_preferences_frozen_at = now(),
				updated_at = now() WHERE id = $1`, controlID, model, effort, tier,
				state.SettingsRevision)
		}
		if err != nil {
			return ConfigurationUpdate{}, err
		}
	}
	update.State = state
	if err := refreshWaitingConfigurationProjectionTx(ctx, tx, state); err != nil {
		return ConfigurationUpdate{}, err
	}
	return update, tx.Commit()
}

func (s *ConversationService) conversationModeState(ctx context.Context, tx *sql.Tx,
	guildID, threadID, userID string, lock bool,
) (ConversationModeState, uuid.UUID, error) {
	query := `SELECT conversation.id, conversation.forum_id,
		conversation.owner_discord_user_id, conversation.lifecycle_state,
		conversation.collaboration_mode, conversation.collaboration_mode_revision,
		conversation.trigger_mode, conversation.trigger_mode_revision,
		COALESCE(conversation.model,''), COALESCE(conversation.reasoning_effort,''),
		COALESCE(conversation.service_tier,'standard'), conversation.settings_revision,
		conversation.status, conversation.configuration_status,
		COALESCE(conversation.configured_by_discord_user_id,''),
		COALESCE(control.id::text, '')
		FROM discord_conversations conversation
		LEFT JOIN codex_thread_controls control
			ON control.discord_conversation_id = conversation.id
		WHERE conversation.guild_id = $1 AND conversation.thread_id = $2`
	if lock {
		query += " FOR UPDATE OF conversation"
	}
	var state ConversationModeState
	var forumID uuid.UUID
	var ownerID, lifecycle, status, configurationStatus, configuredBy, controlRaw string
	err := tx.QueryRowContext(ctx, query, guildID, threadID).Scan(&state.ConversationID,
		&forumID, &ownerID, &lifecycle, &state.Mode, &state.Revision,
		&state.TriggerMode, &state.TriggerRevision, &state.Model, &state.ReasoningEffort,
		&state.ServiceTier, &state.SettingsRevision, &status, &configurationStatus,
		&configuredBy, &controlRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return ConversationModeState{}, uuid.Nil, errors.New("当前频道不是 Codex 会话 Post")
	}
	if err != nil {
		return ConversationModeState{}, uuid.Nil, err
	}
	if lifecycle != "active" {
		return ConversationModeState{}, uuid.Nil, codexcontrol.ErrControlArchived
	}
	state.Awaiting = status == "awaiting_configuration" && configurationStatus != "configured"
	if state.Awaiting {
		if userID != configuredBy {
			return ConversationModeState{}, uuid.Nil, errors.New("等待启动阶段只有 Post 创建者可以修改设置")
		}
	} else if _, err := s.access(ctx, tx, forumID, ownerID, userID); err != nil {
		if errors.Is(err, ErrReadOnly) {
			return ConversationModeState{}, uuid.Nil, errors.New("readonly 用户不能修改会话设置")
		}
		return ConversationModeState{}, uuid.Nil, err
	}
	controlID := uuid.Nil
	if controlRaw != "" {
		controlID, err = uuid.Parse(controlRaw)
		if err != nil {
			return ConversationModeState{}, uuid.Nil, fmt.Errorf("解析 Codex Control: %w", err)
		}
		if lock {
			if err := tx.QueryRowContext(ctx, `SELECT id FROM codex_thread_controls
				WHERE id = $1 FOR UPDATE`, controlID).Scan(&controlID); err != nil {
				return ConversationModeState{}, uuid.Nil, err
			}
		}
	}
	err = tx.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM codex_turn_intents WHERE discord_conversation_id = $1
			AND status IN ('placement_pending','queued','dispatching','awaiting_confirmation',
				'running','waiting_for_user','reconciling','retry_wait')) OR
		EXISTS(SELECT 1 FROM codex_turn_runs run JOIN codex_thread_controls control
			ON control.id = run.control_id WHERE control.discord_conversation_id = $1
			AND run.status IN ('starting','running','waiting_for_user','reconciling'))`,
		state.ConversationID).Scan(&state.Busy)
	return state, controlID, err
}

func conversationModeCard(state ConversationModeState, notice string) ComponentCardPayload {
	body := "**消息触发模式**  `" + triggerModeLabel(state.TriggerMode) + "`\n" +
		triggerModeDescription(state.TriggerMode) + "\n\n**Codex 协作模式**  `" +
		collaborationModeLabel(state.Mode) + "`\n\n" + runtimePreferencesSummary(state)
	if state.Busy {
		body += "\n\n当前 Turn 保持原设置；模型、思考、速度和 Default/Plan 将从下一 Turn 生效。"
	} else if state.Awaiting {
		body = "请调整并确认设置；Post 会一直等待你的明确确认，启动后会将完整设置记为你的新 Post 默认值。\n\n" + body
	}
	if notice != "" {
		body += "\n\n" + notice
	}
	color, header := cardColorBlurple, "⚙️ Codex · 会话设置"
	if state.Awaiting {
		color, header = cardColorYellow, "⚙️ Codex · 即将启动"
	}
	card := ComponentCardPayload{AccentColor: color, Header: header,
		Body: body, Buttons: []ComponentButtonPayload{
			{Label: "交互模式", CustomID: triggerModeButtonID(state, "interactive"),
				Style: "primary", Disabled: state.TriggerMode == "interactive"},
			{Label: "讨论模式", CustomID: triggerModeButtonID(state, "discussion"),
				Disabled: state.TriggerMode == "discussion"},
			{Label: "进入 Plan", CustomID: modeButtonID(state, "plan"), Style: "primary",
				Disabled: state.Mode == "plan"},
			{Label: "退出 Plan", CustomID: modeButtonID(state, "default"),
				Disabled: state.Mode == "default"},
			{Label: "模型 / 思考 / 速度", CustomID: runtimePreferencesButtonID(state),
				Disabled: false},
		}}
	if state.Awaiting {
		card.ButtonRows = [][]ComponentButtonPayload{{{
			Label: "使用当前设置启动", CustomID: configurationStartButtonID(state), Style: "primary",
		}}}
	}
	return card
}

func runtimePreferencesSummary(state ConversationModeState) string {
	model := state.Model
	if model == "" {
		model = "Codex 默认"
	}
	return "**模型**  `" + cardText(model, 128) + "`\n" +
		"**思考等级**  `" + effortLabel(state.ReasoningEffort) + "`\n" +
		"**速度**  `" + serviceTierLabel(state.ServiceTier) + "`"
}

func effortLabel(value string) string {
	switch value {
	case "low":
		return "轻"
	case "medium":
		return "中"
	case "high":
		return "高"
	case "xhigh":
		return "极高"
	default:
		return "Codex 默认"
	}
}

func serviceTierLabel(value string) string {
	if value == "fast" {
		return "快速"
	}
	return "标准"
}

func modeButtonID(state ConversationModeState, target string) string {
	return fmt.Sprintf("codex-mode:%s:%d:%s", state.ConversationID, state.SettingsRevision, target)
}

func triggerModeButtonID(state ConversationModeState, target string) string {
	return fmt.Sprintf("codex-trigger-mode:%s:%d:%s", state.ConversationID,
		state.SettingsRevision, target)
}

func runtimePreferencesButtonID(state ConversationModeState) string {
	return fmt.Sprintf("codex-runtime-config:%s:%d", state.ConversationID,
		state.SettingsRevision)
}

func configurationStartButtonID(state ConversationModeState) string {
	return fmt.Sprintf("codex-config-start:%s:%d", state.ConversationID, state.SettingsRevision)
}

func triggerModeLabel(mode string) string {
	if mode == "discussion" {
		return "讨论模式"
	}
	return "交互模式"
}

func triggerModeDescription(mode string) string {
	if mode == "discussion" {
		return "普通消息只会加入讨论；直接 @ Codex 时才会提交最近的讨论。"
	}
	return "每条用户消息都会立即启动或引导当前 Turn。"
}
