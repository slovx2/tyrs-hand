package httpapi

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/codexsettings"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

func (s *Server) loadWorkerSnapshot(ctx context.Context,
	claimed *codexcontrol.ClaimedControl,
) (workerprotocol.TaskSnapshot, error) {
	var result workerprotocol.TaskSnapshot
	provider, err := s.settings.AgentProvider(ctx)
	if err != nil {
		return result, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT name, COALESCE(model,''),
		COALESCE(reasoning_effort,''), COALESCE(service_tier,''), sandbox,
		approval_policy, network_enabled FROM agent_profiles WHERE id = $1`,
		claimed.AgentProfileID).Scan(&result.Runtime.ProfileName, &result.Runtime.Model,
		&result.Runtime.ReasoningEffort, &result.Runtime.ServiceTier, &result.Runtime.Sandbox,
		&result.Runtime.ApprovalPolicy, &result.Runtime.NetworkEnabled)
	if err != nil {
		return result, err
	}
	result.Runtime.ProviderType = provider.ProviderType
	result.Runtime.BaseURL = provider.BaseURL
	result.Runtime.ProxyURL = provider.ProxyURL
	result.Runtime.ConfigSignature = provider.ConfigSignature
	preferences, err := s.freezeWorkerRuntimePreferences(ctx, claimed)
	if err != nil {
		return result, err
	}
	result.Runtime.Model = preferences.Model
	result.Runtime.ReasoningEffort = preferences.ReasoningEffort
	result.Runtime.ServiceTier = preferences.ServiceTier
	if claimed.SourceType == codexcontrol.SourceGitHub {
		result.GitHub, err = s.loadGitHubWorkerSnapshot(ctx, claimed)
	} else {
		result.Discord, err = s.loadDiscordWorkerSnapshot(ctx, claimed)
	}
	return result, err
}

func (s *Server) freezeWorkerRuntimePreferences(ctx context.Context,
	claimed *codexcontrol.ClaimedControl,
) (codexsettings.EffectivePreferences, error) {
	var result codexsettings.EffectivePreferences
	var model, effort, tier sql.NullString
	var frozen sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT model, reasoning_effort, service_tier,
		runtime_preferences_frozen_at FROM codex_thread_controls WHERE id = $1`, claimed.ControlID).
		Scan(&model, &effort, &tier, &frozen)
	if err != nil {
		return result, err
	}
	if frozen.Valid {
		result.Model, result.ReasoningEffort = model.String, effort.String
		result.ServiceTier = tier.String
		if result.ServiceTier == "" {
			result.ServiceTier = "standard"
		}
		return result, nil
	}
	if claimed.SourceType == codexcontrol.SourceDiscord {
		err = s.db.QueryRowContext(ctx, `SELECT COALESCE(model,''),
			COALESCE(reasoning_effort,''), service_tier FROM discord_conversations WHERE id = $1`,
			claimed.DiscordConversationID).
			Scan(&result.Model, &result.ReasoningEffort, &result.ServiceTier)
	} else {
		result, err = codexsettings.NewService(s.db).Resolve(ctx, claimed.RepositoryID,
			uuid.Nil, claimed.AgentProfileID)
	}
	if err != nil {
		return codexsettings.EffectivePreferences{}, err
	}
	err = s.db.QueryRowContext(ctx, `UPDATE codex_thread_controls SET model = NULLIF($2,''),
		reasoning_effort = NULLIF($3,''), service_tier = $4,
		runtime_preferences_frozen_at = now(), updated_at = now()
		WHERE id = $1 AND runtime_preferences_frozen_at IS NULL
		RETURNING COALESCE(model,''), COALESCE(reasoning_effort,''), service_tier`,
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
	err := s.db.QueryRowContext(ctx, `SELECT r.owner, r.name, r.clone_url, r.default_branch,
		w.kind, w.external_number, COALESCE(w.head_sha,''), COALESCE(w.head_ref,''),
		COALESCE(w.head_repository,''), COALESCE(w.base_sha,''), COALESCE(w.base_ref,''),
		COALESCE(w.html_url,'') FROM repositories r JOIN work_items w ON w.repository_id = r.id
		WHERE r.id = $1 AND w.id = $2`, claimed.RepositoryID, claimed.WorkItemID).Scan(
		&result.Owner, &result.Repository, &result.CloneURL, &result.DefaultBranch,
		&result.Kind, &result.Number, &result.HeadSHA, &result.HeadRef, &result.HeadRepository,
		&result.BaseSHA, &result.BaseRef, &result.HTMLURL)
	return &result, err
}

func (s *Server) loadDiscordWorkerSnapshot(ctx context.Context,
	claimed *codexcontrol.ClaimedControl,
) (*workerprotocol.DiscordSnapshot, error) {
	var result workerprotocol.DiscordSnapshot
	var bindingID sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT c.guild_id, c.thread_id, m.message_id,
		c.owner_discord_user_id, f.id, f.development_environment_id, m.body,
		m.discord_user_id, m.display_name, m.username, COALESCE(m.github_user_id,0),
		COALESCE(m.github_login,''), m.github_binding_id::text, COALESCE(m.binding_version,0),
		m.access_snapshot FROM discord_conversations c
		JOIN discord_input_messages m ON m.conversation_id = c.id
		JOIN discord_forums f ON f.id = c.forum_id AND f.forum_type = 'development'
		WHERE c.id = $1 AND m.message_id = $2`, claimed.DiscordConversationID,
		claimed.DiscordMessageID).Scan(&result.GuildID, &result.ThreadID, &result.MessageID,
		&result.OwnerUserID, &result.ForumID, &result.EnvironmentID, &result.Body,
		&result.UserID, &result.DisplayName, &result.Username, &result.GitHubUserID,
		&result.GitHubLogin, &bindingID, &result.BindingVersion, &result.Access)
	if err != nil {
		return nil, err
	}
	result.BindingID = bindingID.String
	var development workerprotocol.DevelopmentSpec
	development.ConversationID = claimed.DiscordConversationID
	err = s.db.QueryRowContext(ctx, `SELECT e.id, f.id, fw.status, fw.relative_path,
		fw.branch, r.owner || '/' || r.name, r.clone_url, r.default_branch,
		e.build_repository_id, br.owner || '/' || br.name, br.clone_url, br.default_branch,
		e.status, COALESCE(e.image_ref,''), COALESCE(e.image_id,''), e.container_name,
		COALESCE(e.container_id,''), e.data_volume_name, e.home_volume_name, e.network_name,
		COALESCE(e.runtime_user,''), COALESCE(e.runtime_uid,0), COALESCE(e.runtime_gid,0),
		COALESCE(e.runtime_home,''), COALESCE(e.build_source_sha,'')
		FROM discord_forums f
		JOIN discord_development_environments e ON e.id = f.development_environment_id
		JOIN discord_forum_workspaces fw ON fw.forum_id = f.id
		JOIN repositories r ON r.id = f.repository_id
		JOIN repositories br ON br.id = e.build_repository_id WHERE f.id = $1`, result.ForumID).
		Scan(&development.EnvironmentID, &development.ForumID,
			&development.WorkspaceStatus, &development.WorkspaceRelative,
			&development.WorkspaceBranch, &development.Repository, &development.CloneURL,
			&development.DefaultRef, &development.BuildRepositoryID,
			&development.BuildRepository, &development.BuildCloneURL,
			&development.BuildDefaultRef, &development.EnvironmentStatus,
			&development.ImageRef, &development.ImageID, &development.ContainerName,
			&development.ContainerID, &development.DataVolume, &development.HomeVolume,
			&development.Network, &development.RuntimeUser, &development.RuntimeUID,
			&development.RuntimeGID, &development.RuntimeHome, &development.BuildSourceSHA)
	if err != nil {
		return nil, err
	}
	result.Development = &development
	rows, err := s.db.QueryContext(ctx, `SELECT id, kind, original_filename, media_type,
		size_bytes, COALESCE(sha256,'') FROM discord_attachments
		WHERE message_id = $1 AND status = 'ready' ORDER BY created_at, id`, result.MessageID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var item workerprotocol.Attachment
		if err := rows.Scan(&item.ID, &item.Kind, &item.Filename, &item.MediaType, &item.Size,
			&item.SHA256); err != nil {
			return nil, err
		}
		result.Attachments = append(result.Attachments, item)
	}
	return &result, rows.Err()
}
