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
	for _, rule := range []struct {
		name, event, action string
		mention             bool
	}{
		{"mention", "issue_comment", "created", true},
		{"review-requested", "pull_request", "review_requested", false},
	} {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO trigger_rules(repository_id, agent_profile_id, name, event_name, action,
				mention_required, instruction_template, allowed_tools)
			VALUES ($1,$2,$3,$4,$5,$6,
				'Process {{event}} {{action}} for {{owner}}/{{repository}}#{{number}} requested by {{actor}}.\n\n{{body}}',$7)
			ON CONFLICT(repository_id,name) DO NOTHING`,
			repositoryID, profileID, rule.name, rule.event, rule.action, rule.mention, allowedTools)
		if err != nil {
			return err
		}
	}
	return nil
}
