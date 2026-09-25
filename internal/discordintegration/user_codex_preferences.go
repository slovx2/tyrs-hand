package discordintegration

import (
	"context"
	"database/sql"

	"github.com/slovx2/tyrs-hand/internal/codexsettings"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
)

type userCodexPreferences struct {
	Model             string
	ReasoningEffort   string
	ServiceTier       string
	CollaborationMode string
	TriggerMode       string
}

type preferenceQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadUserCodexPreferences(ctx context.Context, queryer preferenceQueryer,
	guildID, userID string, engine runtimeidentity.Engine,
) (userCodexPreferences, bool, error) {
	var value userCodexPreferences
	err := queryer.QueryRowContext(ctx, `SELECT COALESCE(model,''),
		COALESCE(reasoning_effort,''), service_tier, collaboration_mode, trigger_mode
		FROM discord_user_codex_preferences
		WHERE guild_id = $1 AND discord_user_id = $2 AND engine=$3`, guildID, userID, engine).
		Scan(&value.Model, &value.ReasoningEffort, &value.ServiceTier,
			&value.CollaborationMode, &value.TriggerMode)
	if err == sql.ErrNoRows {
		return userCodexPreferences{}, false, nil
	}
	return value, err == nil, err
}

func applyUserCodexPreferences(effective *codexsettings.EffectivePreferences,
	value userCodexPreferences,
) {
	effective.Model = value.Model
	effective.ReasoningEffort = value.ReasoningEffort
	effective.ServiceTier = value.ServiceTier
}

func saveUserCodexPreferences(ctx context.Context, execer discordOutboxExecer,
	guildID, userID string, engine runtimeidentity.Engine, value userCodexPreferences,
) error {
	_, err := execer.ExecContext(ctx, `INSERT INTO discord_user_codex_preferences
		(guild_id, discord_user_id, model, reasoning_effort, service_tier,
		 collaboration_mode, trigger_mode, engine)
		VALUES ($1,$2,NULLIF($3,''),NULLIF($4,''),$5,$6,$7,$8)
		ON CONFLICT(guild_id, discord_user_id, engine) DO UPDATE SET
		model = EXCLUDED.model, reasoning_effort = EXCLUDED.reasoning_effort,
		service_tier = EXCLUDED.service_tier,
		collaboration_mode = EXCLUDED.collaboration_mode,
		trigger_mode = EXCLUDED.trigger_mode, updated_at = now()`,
		guildID, userID, value.Model, value.ReasoningEffort, value.ServiceTier,
		value.CollaborationMode, value.TriggerMode, engine)
	return err
}
