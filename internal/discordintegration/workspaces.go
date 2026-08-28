package discordintegration

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

type Workspace struct {
	ID                uuid.UUID          `json:"id"`
	OwnerUserID       string             `json:"ownerDiscordUserId"`
	OwnerName         string             `json:"ownerName"`
	WorkerID          *uuid.UUID         `json:"workerId,omitempty"`
	ProjectsScannedAt *time.Time         `json:"projectsScannedAt,omitempty"`
	ProjectScanError  string             `json:"projectScanError,omitempty"`
	Projects          []WorkspaceProject `json:"projects"`
}

type WorkspaceForum struct {
	ID            uuid.UUID     `json:"id"`
	Name          string        `json:"name"`
	DiscordID     string        `json:"discordId"`
	BindingStatus string        `json:"bindingStatus"`
	Collaborators []ForumAccess `json:"collaborators"`
}

type WorkspaceProject struct {
	ID                  uuid.UUID        `json:"id"`
	Name                string           `json:"name"`
	RelativePath        string           `json:"relativePath"`
	ProjectSource       string           `json:"projectSource"`
	HostPath            string           `json:"hostPath,omitempty"`
	DesiredRelativePath string           `json:"desiredRelativePath,omitempty"`
	ProjectKind         string           `json:"projectKind"`
	AvailabilityStatus  string           `json:"availabilityStatus"`
	Branch              string           `json:"branch,omitempty"`
	HeadSHA             string           `json:"headSha,omitempty"`
	Dirty               bool             `json:"dirty"`
	RemoteURL           string           `json:"remoteUrl,omitempty"`
	LastSeenAt          time.Time        `json:"lastSeenAt"`
	ScanError           string           `json:"scanError,omitempty"`
	Forums              []WorkspaceForum `json:"forums"`
}

func (m *Manager) Workspaces(ctx context.Context) ([]Workspace, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT e.id, e.owner_discord_user_id,
		COALESCE(NULLIF(dm.display_name, ''), dm.username), e.worker_id::text,
		e.projects_scanned_at, COALESCE(e.project_scan_error,'')
		FROM worker_workspaces e
		JOIN discord_members dm ON dm.guild_id = e.guild_id AND dm.discord_user_id = e.owner_discord_user_id
		ORDER BY lower(COALESCE(NULLIF(dm.display_name,''),dm.username)), e.created_at`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make([]Workspace, 0)
	for rows.Next() {
		var environment Workspace
		var workerID sql.NullString
		var projectsScannedAt sql.NullTime
		if err := rows.Scan(&environment.ID, &environment.OwnerUserID, &environment.OwnerName,
			&workerID, &projectsScannedAt,
			&environment.ProjectScanError); err != nil {
			return nil, err
		}
		if workerID.Valid {
			id, parseErr := uuid.Parse(workerID.String)
			if parseErr != nil {
				return nil, parseErr
			}
			environment.WorkerID = &id
		}
		if projectsScannedAt.Valid {
			environment.ProjectsScannedAt = &projectsScannedAt.Time
		}
		environment.Projects = []WorkspaceProject{}
		result = append(result, environment)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range result {
		projects, err := m.workspaceProjects(ctx, result[index].ID)
		if err != nil {
			return nil, err
		}
		result[index].Projects = projects
	}
	return result, nil
}

func (m *Manager) WorkspaceForWorker(ctx context.Context,
	workerID uuid.UUID,
) (*Workspace, error) {
	var workspace Workspace
	var persistedWorkerID sql.NullString
	var projectsScannedAt sql.NullTime
	err := m.db.QueryRowContext(ctx, `SELECT e.id, e.owner_discord_user_id,
		COALESCE(NULLIF(dm.display_name, ''), dm.username), e.worker_id::text,
		e.projects_scanned_at, COALESCE(e.project_scan_error,'')
		FROM worker_workspaces e
		JOIN discord_members dm ON dm.guild_id = e.guild_id
			AND dm.discord_user_id = e.owner_discord_user_id
		WHERE e.worker_id=$1`, workerID).Scan(&workspace.ID, &workspace.OwnerUserID,
		&workspace.OwnerName, &persistedWorkerID, &projectsScannedAt,
		&workspace.ProjectScanError)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if persistedWorkerID.Valid {
		id, parseErr := uuid.Parse(persistedWorkerID.String)
		if parseErr != nil {
			return nil, parseErr
		}
		workspace.WorkerID = &id
	}
	if projectsScannedAt.Valid {
		workspace.ProjectsScannedAt = &projectsScannedAt.Time
	}
	workspace.Projects, err = m.workspaceProjects(ctx, workspace.ID)
	if err != nil {
		return nil, err
	}
	return &workspace, nil
}

func (m *Manager) workspaceProjects(ctx context.Context,
	workspaceID uuid.UUID,
) ([]WorkspaceProject, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT project.id, project.name,
		project.relative_path, COALESCE(project.desired_relative_path,''),
		COALESCE(project.project_source,'workspace_child'), COALESCE(project.host_path,''),
		project.project_kind, project.availability_status, COALESCE(project.branch,''),
		COALESCE(project.head_sha,''), project.dirty, COALESCE(project.remote_url,''),
		project.last_seen_at, COALESCE(project.scan_error,''),
		forum.id::text, COALESCE(resource.name,''), COALESCE(resource.discord_id,''),
		COALESCE(forum.binding_status,'')
		FROM workspace_projects project
		LEFT JOIN discord_forums forum ON forum.workspace_project_id=project.id
			AND forum.forum_type='workspace'
		LEFT JOIN discord_resources resource ON resource.id=forum.resource_id
		WHERE project.workspace_id=$1
		ORDER BY lower(project.name), project.relative_path, forum.created_at`,
		workspaceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make([]WorkspaceProject, 0)
	byID := make(map[uuid.UUID]int)
	for rows.Next() {
		var project WorkspaceProject
		var forumID sql.NullString
		var forum WorkspaceForum
		if err := rows.Scan(&project.ID, &project.Name, &project.RelativePath,
			&project.DesiredRelativePath, &project.ProjectSource, &project.HostPath,
			&project.ProjectKind, &project.AvailabilityStatus,
			&project.Branch, &project.HeadSHA, &project.Dirty, &project.RemoteURL,
			&project.LastSeenAt, &project.ScanError, &forumID, &forum.Name,
			&forum.DiscordID, &forum.BindingStatus); err != nil {
			return nil, err
		}
		index, exists := byID[project.ID]
		if !exists {
			project.Forums = []WorkspaceForum{}
			result = append(result, project)
			index = len(result) - 1
			byID[project.ID] = index
		}
		if forumID.Valid {
			forum.ID, err = uuid.Parse(forumID.String)
			if err != nil {
				return nil, err
			}
			forum.Collaborators, err = m.ForumAccess(ctx, forum.ID)
			if err != nil {
				return nil, err
			}
			result[index].Forums = append(result[index].Forums, forum)
		}
	}
	return result, rows.Err()
}

func (m *Manager) CreateWorkspace(ctx context.Context, ownerID string,
	workerID uuid.UUID,
) (uuid.UUID, error) {
	settings, err := m.Settings(ctx)
	if err != nil || settings.GuildID == "" {
		return uuid.Nil, errors.New("discord Guild 尚未配置")
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return uuid.Nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var eligible bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM discord_members member
		WHERE member.guild_id=$1 AND member.discord_user_id=$2 AND member.active
		  AND NOT EXISTS (
			SELECT 1 FROM worker_workspaces workspace
			WHERE workspace.guild_id=member.guild_id
			  AND workspace.owner_discord_user_id=member.discord_user_id))`,
		settings.GuildID, ownerID).Scan(&eligible); err != nil {
		return uuid.Nil, err
	}
	if !eligible {
		return uuid.Nil, errors.New("成员不活跃或已经拥有 Workspace")
	}
	workspaceID := uuid.New()
	var workerEligible bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workers
		WHERE id=$1 AND enabled AND roles ? 'discord')`, workerID).Scan(&workerEligible)
	if err != nil {
		return uuid.Nil, err
	}
	if !workerEligible {
		return uuid.Nil, errors.New("worker 不存在、已停用或不支持 Discord")
	}
	var workerAvailable bool
	if err := tx.QueryRowContext(ctx, `SELECT NOT EXISTS(
			SELECT 1 FROM worker_workspaces WHERE worker_id=$1)`, workerID).Scan(&workerAvailable); err != nil {
		return uuid.Nil, err
	}
	if !workerAvailable {
		return uuid.Nil, errors.New("worker 已绑定 Workspace")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO worker_workspaces
		(id,guild_id,owner_discord_user_id,worker_id)
		VALUES ($1,$2,$3,$4)`,
		workspaceID, settings.GuildID, ownerID, workerID)
	if err != nil {
		return uuid.Nil, err
	}
	return workspaceID, tx.Commit()
}
