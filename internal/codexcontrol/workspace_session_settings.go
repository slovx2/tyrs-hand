package codexcontrol

import (
	"context"
	"database/sql"

	"github.com/google/uuid"
)

// ProjectWorkspaceSessionSettingsTx 只把服务端 Session 真源投影到运行控制和外部界面。
// 投影表不得通过这条路径反向修改 workspace_sessions。
func ProjectWorkspaceSessionSettingsTx(ctx context.Context, tx *sql.Tx,
	sessionID uuid.UUID,
) error {
	if _, err := tx.ExecContext(ctx, `UPDATE codex_thread_controls control SET
		agent_profile_id=session.agent_profile_id,
		model=session.model,
		reasoning_effort=session.reasoning_effort,
		service_tier=session.service_tier,
		collaboration_mode_revision=collaboration_mode_revision +
			CASE WHEN control.collaboration_mode=session.collaboration_mode THEN 0 ELSE 1 END,
		collaboration_mode=session.collaboration_mode,
		settings_revision=GREATEST(control.settings_revision,session.settings_version),
		runtime_preferences_frozen_at=now(),updated_at=now()
		FROM workspace_sessions session
		WHERE session.id=$1 AND control.session_id=session.id`, sessionID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE discord_conversations conversation SET
		agent_profile_id=session.agent_profile_id,
		model=session.model,
		reasoning_effort=session.reasoning_effort,
		service_tier=session.service_tier,
		collaboration_mode_revision=collaboration_mode_revision +
			CASE WHEN conversation.collaboration_mode=session.collaboration_mode THEN 0 ELSE 1 END,
		collaboration_mode=session.collaboration_mode,
		settings_revision=GREATEST(conversation.settings_revision,session.settings_version),
		updated_at=now()
		FROM workspace_sessions session
		WHERE session.id=$1 AND conversation.session_id=session.id`, sessionID)
	return err
}
