package discordintegration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
)

type ConversationModeState struct {
	ConversationID uuid.UUID
	Mode           string
	Revision       int64
	Busy           bool
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
) (ConversationModeState, bool, error) {
	if target != "default" && target != "plan" {
		return ConversationModeState{}, false, errors.New("目标模式无效")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ConversationModeState{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	state, controlID, err := s.conversationModeState(ctx, tx, guildID, threadID, userID, true)
	if err != nil {
		return ConversationModeState{}, false, err
	}
	if state.ConversationID != expectedConversationID {
		return ConversationModeState{}, false, errors.New("这个模式按钮不属于当前 Codex 会话")
	}
	if state.Revision != expectedRevision {
		return state, true, tx.Commit()
	}
	if state.Busy {
		return state, false, errors.New("当前会话有排队、运行或等待回答的 Turn，暂时不能切换模式")
	}
	if state.Mode != target {
		state.Mode = target
		state.Revision++
		_, err = tx.ExecContext(ctx, `UPDATE discord_conversations SET collaboration_mode = $2,
			collaboration_mode_revision = $3, updated_at = now() WHERE id = $1`,
			state.ConversationID, state.Mode, state.Revision)
		if err == nil && controlID != uuid.Nil {
			_, err = tx.ExecContext(ctx, `UPDATE codex_thread_controls SET collaboration_mode = $2,
				collaboration_mode_revision = $3, updated_at = now() WHERE id = $1`,
				controlID, state.Mode, state.Revision)
		}
		if err != nil {
			return ConversationModeState{}, false, err
		}
	}
	return state, false, tx.Commit()
}

func (s *ConversationService) conversationModeState(ctx context.Context, tx *sql.Tx,
	guildID, threadID, userID string, lock bool,
) (ConversationModeState, uuid.UUID, error) {
	query := `SELECT conversation.id, conversation.forum_id,
		conversation.owner_discord_user_id, conversation.lifecycle_state,
		conversation.collaboration_mode, conversation.collaboration_mode_revision,
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
	var ownerID, lifecycle, controlRaw string
	err := tx.QueryRowContext(ctx, query, guildID, threadID).Scan(&state.ConversationID,
		&forumID, &ownerID, &lifecycle, &state.Mode, &state.Revision, &controlRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return ConversationModeState{}, uuid.Nil, errors.New("当前频道不是 Codex 会话 Post")
	}
	if err != nil {
		return ConversationModeState{}, uuid.Nil, err
	}
	if lifecycle != "active" {
		return ConversationModeState{}, uuid.Nil, codexcontrol.ErrControlArchived
	}
	if _, err := s.access(ctx, tx, forumID, ownerID, userID); err != nil {
		if errors.Is(err, ErrReadOnly) {
			return ConversationModeState{}, uuid.Nil, errors.New("readonly 用户不能切换会话模式")
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
	body := "**当前模式**  `" + collaborationModeLabel(state.Mode) + "`"
	if state.Busy {
		body += "\n\n当前有排队、运行或等待回答的 Turn，完成或停止后才能切换。"
	}
	if notice != "" {
		body += "\n\n" + notice
	}
	return ComponentCardPayload{AccentColor: cardColorBlurple, Header: "🧭 Codex · 会话模式",
		Body: body, Buttons: []ComponentButtonPayload{
			{Label: "进入 Plan", CustomID: modeButtonID(state, "plan"), Style: "primary",
				Disabled: state.Busy || state.Mode == "plan"},
			{Label: "退出 Plan", CustomID: modeButtonID(state, "default"),
				Disabled: state.Busy || state.Mode == "default"},
		}}
}

func modeButtonID(state ConversationModeState, target string) string {
	return fmt.Sprintf("codex-mode:%s:%d:%s", state.ConversationID, state.Revision, target)
}
