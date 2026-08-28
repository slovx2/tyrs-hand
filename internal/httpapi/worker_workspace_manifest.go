package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"path"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/slovx2/tyrs-hand/internal/participantidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

var errInvalidWorkspaceProjectScan = errors.New("开发项目快照无效")

func (s *Server) saveWorkspaceProjectScan(ctx context.Context, workerID,
	workspaceID uuid.UUID, scan workerprotocol.WorkspaceProjectScanResult,
) error {
	if workerID == uuid.Nil || workspaceID == uuid.Nil || len(scan.Projects) > 5000 ||
		len(scan.ScanError) > 2000 {
		return errInvalidWorkspaceProjectScan
	}
	if scan.ScanError != "" {
		result, err := s.db.ExecContext(ctx,
			`UPDATE worker_workspaces
			SET project_scan_error=$3, updated_at=now()
			WHERE id=$1 AND worker_id=$2`,
			workspaceID, workerID, scan.ScanError)
		if err != nil {
			return fmt.Errorf("保存开发项目扫描失败状态: %w", err)
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return sql.ErrNoRows
		}
		return nil
	}
	projects := append([]workerprotocol.WorkspaceProjectSnapshot(nil), scan.Projects...)
	seen := make(map[string]struct{}, len(projects))
	paths := make([]string, 0, len(projects))
	for index := range projects {
		project := &projects[index]
		cleanPath := path.Clean(strings.TrimSpace(project.RelativePath))
		name := strings.TrimSpace(project.Name)
		validSource := project.ProjectSource == "workspace_root" || project.ProjectSource == "workspace_child" || project.ProjectSource == "codex_registered"
		validPath := (project.ProjectSource == "workspace_root" && cleanPath == "workspaces" && name == "Workspace") ||
			(project.ProjectSource == "workspace_child" && cleanPath == path.Join("workspaces", name)) ||
			(project.ProjectSource == "codex_registered" && strings.HasPrefix(cleanPath, "codex/") && len(strings.TrimPrefix(cleanPath, "codex/")) == 64)
		if !validSource || !validPath || name == "" ||
			strings.HasPrefix(name, ".") || strings.Contains(name, "/") ||
			!filepath.IsAbs(project.HostPath) ||
			(project.ProjectKind != "directory" && project.ProjectKind != "git") ||
			(project.ProjectKind == "directory" &&
				(project.Branch != "" || project.HeadSHA != "" || project.RemoteURL != "")) {
			return fmt.Errorf("%w: 条目无效", errInvalidWorkspaceProjectScan)
		}
		if _, duplicate := seen[cleanPath]; duplicate {
			return fmt.Errorf("%w: 包含重复路径", errInvalidWorkspaceProjectScan)
		}
		project.Name, project.RelativePath = name, cleanPath
		seen[cleanPath] = struct{}{}
		paths = append(paths, cleanPath)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("保存开发项目快照: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var owned bool
	if err := tx.QueryRowContext(ctx, `SELECT true
		FROM worker_workspaces
		WHERE id=$1 AND worker_id=$2 FOR UPDATE`,
		workspaceID, workerID).Scan(&owned); err != nil {
		return err
	}
	for _, project := range projects {
		if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_projects
			(workspace_id,relative_path,name,project_kind,branch,head_sha,dirty,
				remote_url,availability_status,project_source,host_path,scan_error,last_seen_at)
			VALUES ($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''),$7,NULLIF($8,''),
				CASE WHEN $9 THEN 'available' ELSE 'missing' END,$10,NULLIF($11,''),NULLIF($12,''),now())
			ON CONFLICT(workspace_id,relative_path) DO UPDATE SET
				name=EXCLUDED.name, project_kind=EXCLUDED.project_kind,
				branch=EXCLUDED.branch, head_sha=EXCLUDED.head_sha,
				dirty=EXCLUDED.dirty, remote_url=EXCLUDED.remote_url,
				availability_status=EXCLUDED.availability_status, project_source=EXCLUDED.project_source,
				host_path=EXCLUDED.host_path, scan_error=EXCLUDED.scan_error, last_seen_at=now(),
				updated_at=now()`,
			workspaceID, project.RelativePath, project.Name, project.ProjectKind,
			project.Branch, project.HeadSHA, project.Dirty, project.RemoteURL, project.Available,
			project.ProjectSource, project.HostPath, project.ScanError); err != nil {
			return fmt.Errorf("写入开发项目快照: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workspace_projects
		SET availability_status='missing', updated_at=now()
		WHERE workspace_id=$1 AND relative_path <> ALL($2::text[])`,
		workspaceID, pq.Array(paths)); err != nil {
		return fmt.Errorf("同步缺失开发项目: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE worker_workspaces
		SET projects_scanned_at=now(), project_scan_error=NULL, updated_at=now()
		WHERE id=$1`, workspaceID); err != nil {
		return fmt.Errorf("更新开发项目扫描时间: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交开发项目快照: %w", err)
	}
	return nil
}

func (s *Server) workerWorkspace(c *gin.Context) {
	rows, err := s.db.QueryContext(c.Request.Context(), `SELECT id,
		e.owner_discord_user_id,
		COALESCE(NULLIF(m.display_name, ''), m.username, ''), e.guild_id
		FROM worker_workspaces e
		LEFT JOIN discord_members m ON m.guild_id = e.guild_id
			AND m.discord_user_id = e.owner_discord_user_id
		WHERE worker_id = $1
		ORDER BY created_at, id`, currentWorker(c).ID)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取Workspace Manifest 失败", err)
		return
	}
	defer func() { _ = rows.Close() }()
	var workspace *workerprotocol.WorkspaceManifest
	for rows.Next() {
		if workspace != nil {
			problem(c, http.StatusConflict, "Worker 绑定了多个 Workspace", errors.New("worker workspace 绑定不唯一"))
			return
		}
		item := workerprotocol.WorkspaceManifest{}
		var ownerUserID, ownerDisplayName, guildID string
		if err := rows.Scan(&item.WorkspaceID, &ownerUserID, &ownerDisplayName, &guildID); err != nil {
			problem(c, http.StatusInternalServerError, "解析Workspace Manifest 失败", err)
			return
		}
		if ownerUserID != "" {
			item.OwnerParticipant = &workerprotocol.ParticipantIdentity{
				ParticipantID: participantidentity.ID(guildID, ownerUserID),
				DiscordUserID: ownerUserID, DisplayName: ownerDisplayName,
			}
		}
		workspace = &item
	}
	if err := rows.Close(); err != nil {
		problem(c, http.StatusInternalServerError, "读取Workspace Manifest 失败", err)
		return
	}
	if workspace != nil {
		forums, err := s.workspaceManifestForums(c, workspace.WorkspaceID)
		if err != nil {
			problem(c, http.StatusInternalServerError, "读取Workspace Forum 失败", err)
			return
		}
		workspace.Forums = forums
	}
	c.JSON(http.StatusOK, gin.H{"workspace": workspace})
}

func (s *Server) workspaceManifestForums(c *gin.Context,
	workspaceID uuid.UUID,
) ([]workerprotocol.WorkspaceForum, error) {
	rows, err := s.db.QueryContext(c.Request.Context(), `SELECT f.id, f.guild_id,
		r.discord_id, f.owner_discord_user_id, project.id::text,
		project.project_kind, project.relative_path, 'ready'
		FROM discord_forums f JOIN discord_resources r ON r.id = f.resource_id
		JOIN workspace_projects project ON project.id=f.workspace_project_id
		WHERE f.workspace_id = $1
			AND f.binding_status='active'
			AND project.availability_status='available'
		ORDER BY project.relative_path, f.id`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make([]workerprotocol.WorkspaceForum, 0)
	for rows.Next() {
		var item workerprotocol.WorkspaceForum
		var projectID sql.NullString
		if err := rows.Scan(&item.ForumID, &item.GuildID, &item.DiscordForumID,
			&item.OwnerUserID, &projectID, &item.WorkspaceKind, &item.WorkspaceRelative,
			&item.WorkspaceStatus); err != nil {
			return nil, err
		}
		if project := parseOptionalUUID(projectID); project != uuid.Nil {
			item.ProjectID = &project
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
