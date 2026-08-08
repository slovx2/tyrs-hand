package httpapi

import (
	"context"
	"database/sql"
	"errors"

	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/codexsettings"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

func (s *Server) loadWorkerSnapshot(ctx context.Context,
	claimed *codexcontrol.ClaimedControl,
) (workerprotocol.TaskSnapshot, error) {
	var result workerprotocol.TaskSnapshot
	if claimed.SourceType != codexcontrol.SourceGitHub {
		return result, errors.New("worker 快照只支持 github_work_item")
	}
	if err := s.db.QueryRowContext(ctx, `SELECT name,COALESCE(model,''),
		COALESCE(reasoning_effort,''),COALESCE(service_tier,''),sandbox,
		approval_policy,network_enabled FROM agent_profiles WHERE id=$1`,
		claimed.AgentProfileID).Scan(&result.Runtime.ProfileName, &result.Runtime.Model,
		&result.Runtime.ReasoningEffort, &result.Runtime.ServiceTier,
		&result.Runtime.Sandbox, &result.Runtime.ApprovalPolicy,
		&result.Runtime.NetworkEnabled); err != nil {
		return result, err
	}
	instructions, err := s.settings.GitHubAgentInstructions(ctx)
	if err != nil {
		return result, err
	}
	result.GitHubAgent = &workerprotocol.GitHubAgentSnapshot{Instructions: instructions.Content}
	preferences, err := s.freezeWorkerRuntimePreferences(ctx, claimed)
	if err != nil {
		return result, err
	}
	result.Runtime.Model = preferences.Model
	result.Runtime.ReasoningEffort = preferences.ReasoningEffort
	result.Runtime.ServiceTier = preferences.ServiceTier
	result.GitHub, err = s.loadGitHubWorkerSnapshot(ctx, claimed)
	return result, err
}

func (s *Server) freezeWorkerRuntimePreferences(ctx context.Context,
	claimed *codexcontrol.ClaimedControl,
) (codexsettings.EffectivePreferences, error) {
	var result codexsettings.EffectivePreferences
	var model, effort, tier sql.NullString
	var frozen sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT model,reasoning_effort,service_tier,
		runtime_preferences_frozen_at FROM codex_thread_controls WHERE id=$1`,
		claimed.ControlID).Scan(&model, &effort, &tier, &frozen)
	if err != nil {
		return result, err
	}
	if frozen.Valid {
		result.Model, result.ReasoningEffort, result.ServiceTier =
			model.String, effort.String, tier.String
		if result.ServiceTier == "" {
			result.ServiceTier = "standard"
		}
		return result, nil
	}
	result, err = codexsettings.NewService(s.db).Resolve(ctx, claimed.RepositoryID,
		claimed.AgentProfileID)
	if err != nil {
		return codexsettings.EffectivePreferences{}, err
	}
	err = s.db.QueryRowContext(ctx, `UPDATE codex_thread_controls SET model=NULLIF($2,''),
		reasoning_effort=NULLIF($3,''),service_tier=$4,
		runtime_preferences_frozen_at=now(),updated_at=now()
		WHERE id=$1 AND runtime_preferences_frozen_at IS NULL
		RETURNING COALESCE(model,''),COALESCE(reasoning_effort,''),service_tier`,
		claimed.ControlID, result.Model, result.ReasoningEffort, result.ServiceTier).
		Scan(&result.Model, &result.ReasoningEffort, &result.ServiceTier)
	if errors.Is(err, sql.ErrNoRows) {
		return s.freezeWorkerRuntimePreferences(ctx, claimed)
	}
	return result, err
}

func (s *Server) loadGitHubWorkerSnapshot(ctx context.Context,
	claimed *codexcontrol.ClaimedControl,
) (*workerprotocol.GitHubSnapshot, error) {
	var result workerprotocol.GitHubSnapshot
	err := s.db.QueryRowContext(ctx, `SELECT r.owner,r.name,r.clone_url,r.default_branch,
		w.kind,w.external_number,COALESCE(w.head_sha,''),COALESCE(w.head_ref,''),
		COALESCE(w.head_repository,''),COALESCE(w.base_sha,''),COALESCE(w.base_ref,''),
		COALESCE(w.html_url,'') FROM repositories r JOIN work_items w ON w.repository_id=r.id
		WHERE r.id=$1 AND w.id=$2`, claimed.RepositoryID, claimed.WorkItemID).Scan(
		&result.Owner, &result.Repository, &result.CloneURL, &result.DefaultBranch,
		&result.Kind, &result.Number, &result.HeadSHA, &result.HeadRef,
		&result.HeadRepository, &result.BaseSHA, &result.BaseRef, &result.HTMLURL)
	return &result, err
}
