package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"path"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/slovx2/tyrs-hand/internal/participantidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

func (s *Server) workerWorkspaceProjectSnapshot(c *gin.Context) {
	var request workerprotocol.WorkspaceProjectSnapshotRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	workspaceID := request.WorkspaceID
	if workspaceID == uuid.Nil || len(request.Projects) > 5000 ||
		len(request.Error) > 2000 {
		badRequest(c, errors.New("开发项目快照无效"))
		return
	}
	if request.Error != "" {
		result, err := s.db.ExecContext(c.Request.Context(),
			`UPDATE worker_workspaces
			SET project_scan_error=$3, updated_at=now()
			WHERE id=$1 AND worker_id=$2`,
			workspaceID, currentWorker(c).ID, request.Error)
		if err != nil {
			problem(c, http.StatusInternalServerError, "保存开发项目扫描失败状态失败", err)
			return
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			problem(c, http.StatusNotFound, "Workspace不存在", sql.ErrNoRows)
			return
		}
		c.Status(http.StatusNoContent)
		return
	}
	seen := make(map[string]struct{}, len(request.Projects))
	paths := make([]string, 0, len(request.Projects))
	for index := range request.Projects {
		project := &request.Projects[index]
		cleanPath := path.Clean(strings.TrimSpace(project.RelativePath))
		name := strings.TrimSpace(project.Name)
		if cleanPath != path.Join("workspaces", name) || name == "" ||
			strings.HasPrefix(name, ".") || strings.Contains(name, "/") ||
			(project.ProjectKind != "directory" && project.ProjectKind != "git") ||
			(project.ProjectKind == "directory" &&
				(project.Branch != "" || project.HeadSHA != "" || project.RemoteURL != "")) {
			badRequest(c, errors.New("开发项目快照条目无效"))
			return
		}
		if _, duplicate := seen[cleanPath]; duplicate {
			badRequest(c, errors.New("开发项目快照包含重复路径"))
			return
		}
		project.Name, project.RelativePath = name, cleanPath
		seen[cleanPath] = struct{}{}
		paths = append(paths, cleanPath)
	}
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "保存开发项目快照失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var owned bool
	if err := tx.QueryRowContext(c.Request.Context(), `SELECT true
		FROM worker_workspaces
		WHERE id=$1 AND worker_id=$2 FOR UPDATE`,
		workspaceID, currentWorker(c).ID).Scan(&owned); err != nil {
		problem(c, http.StatusNotFound, "Workspace不存在", err)
		return
	}
	for _, project := range request.Projects {
		if _, err := tx.ExecContext(c.Request.Context(), `INSERT INTO workspace_projects
			(workspace_id,relative_path,name,project_kind,branch,head_sha,dirty,
				remote_url,availability_status,last_seen_at)
			VALUES ($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''),$7,NULLIF($8,''),
				'available',now())
			ON CONFLICT(workspace_id,relative_path) DO UPDATE SET
				name=EXCLUDED.name, project_kind=EXCLUDED.project_kind,
				branch=EXCLUDED.branch, head_sha=EXCLUDED.head_sha,
				dirty=EXCLUDED.dirty, remote_url=EXCLUDED.remote_url,
				availability_status='available', scan_error=NULL, last_seen_at=now(),
				updated_at=now()`,
			workspaceID, project.RelativePath, project.Name, project.ProjectKind,
			project.Branch, project.HeadSHA, project.Dirty, project.RemoteURL); err != nil {
			problem(c, http.StatusInternalServerError, "写入开发项目快照失败", err)
			return
		}
	}
	if _, err := tx.ExecContext(c.Request.Context(), `UPDATE workspace_projects
		SET availability_status='missing', updated_at=now()
		WHERE workspace_id=$1 AND relative_path <> ALL($2::text[])`,
		workspaceID, pq.Array(paths)); err != nil {
		problem(c, http.StatusInternalServerError, "同步缺失开发项目失败", err)
		return
	}
	if _, err := tx.ExecContext(c.Request.Context(),
		`UPDATE worker_workspaces
		SET projects_scanned_at=now(), project_scan_error=NULL, updated_at=now()
		WHERE id=$1`, workspaceID); err != nil {
		problem(c, http.StatusInternalServerError, "更新开发项目扫描时间失败", err)
		return
	}
	if err := tx.Commit(); err != nil {
		problem(c, http.StatusInternalServerError, "提交开发项目快照失败", err)
		return
	}
	c.Status(http.StatusNoContent)
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
