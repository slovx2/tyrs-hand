package orchestrator

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
)

func SeedRepositoryRules(ctx context.Context, tx *sql.Tx, repositoryID uuid.UUID) error {
	var profileID uuid.UUID
	var allowedTools []byte
	err := tx.QueryRowContext(ctx, `SELECT id, allowed_tools FROM agent_profiles ORDER BY created_at LIMIT 1`).Scan(&profileID, &allowedTools)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `INSERT INTO agent_profiles(name, allowed_tools) VALUES ('Default', '[]'::jsonb) RETURNING id, allowed_tools`).Scan(&profileID, &allowedTools)
	}
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO trigger_rules(repository_id, agent_profile_id, name, event_name, action,
			trigger_kind, trigger_value, instruction_template, allowed_tools)
		VALUES
			($1,$2,'command','issue_comment','created','slash_command','tyrs-hand',
				'Process {{event}} {{action}} for {{owner}}/{{repository}}#{{number}} requested by {{actor}}.\n\n{{body}}',$3),
			($1,$2,'label-issue','issues','labeled','label','tyrs-hand',
				'Process {{event}} {{action}} for {{owner}}/{{repository}}#{{number}} requested by {{actor}}.\n\n{{body}}',$3),
			($1,$2,'label-pull-request','pull_request','labeled','label','tyrs-hand',
				'Process {{event}} {{action}} for {{owner}}/{{repository}}#{{number}} requested by {{actor}}.\n\n{{body}}',$3)
		ON CONFLICT(repository_id,name) DO NOTHING`, repositoryID, profileID, allowedTools)
	return err
}
