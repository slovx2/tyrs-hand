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

func (s *Server) workerDevelopmentProjectSnapshot(c *gin.Context) {
	environmentID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	var request workerprotocol.DevelopmentProjectSnapshotRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	if request.EnvironmentID != environmentID || len(request.Projects) > 5000 ||
		len(request.Error) > 2000 {
		badRequest(c, errors.New("开发项目快照无效"))
		return
	}
	if request.Error != "" {
		result, err := s.db.ExecContext(c.Request.Context(),
			`UPDATE discord_development_environments
			SET project_scan_error=$3, updated_at=now()
			WHERE id=$1 AND execution_node_id=$2`,
			environmentID, workerNode(c).ID, request.Error)
		if err != nil {
			problem(c, http.StatusInternalServerError, "保存开发项目扫描失败状态失败", err)
			return
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			problem(c, http.StatusNotFound, "开发环境不存在", sql.ErrNoRows)
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
		FROM discord_development_environments
		WHERE id=$1 AND execution_node_id=$2 FOR UPDATE`,
		environmentID, workerNode(c).ID).Scan(&owned); err != nil {
		problem(c, http.StatusNotFound, "开发环境不存在", err)
		return
	}
	for _, project := range request.Projects {
		if _, err := tx.ExecContext(c.Request.Context(), `INSERT INTO development_projects
			(environment_id,relative_path,name,project_kind,branch,head_sha,dirty,
				remote_url,availability_status,last_seen_at)
			VALUES ($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''),$7,NULLIF($8,''),
				'available',now())
			ON CONFLICT(environment_id,relative_path) DO UPDATE SET
				name=EXCLUDED.name, project_kind=EXCLUDED.project_kind,
				branch=EXCLUDED.branch, head_sha=EXCLUDED.head_sha,
				dirty=EXCLUDED.dirty, remote_url=EXCLUDED.remote_url,
				availability_status='available', scan_error=NULL, last_seen_at=now(),
				updated_at=now()`,
			environmentID, project.RelativePath, project.Name, project.ProjectKind,
			project.Branch, project.HeadSHA, project.Dirty, project.RemoteURL); err != nil {
			problem(c, http.StatusInternalServerError, "写入开发项目快照失败", err)
			return
		}
	}
	if _, err := tx.ExecContext(c.Request.Context(), `UPDATE development_projects
		SET availability_status='missing', updated_at=now()
		WHERE environment_id=$1 AND relative_path <> ALL($2::text[])`,
		environmentID, pq.Array(paths)); err != nil {
		problem(c, http.StatusInternalServerError, "同步缺失开发项目失败", err)
		return
	}
	if _, err := tx.ExecContext(c.Request.Context(),
		`UPDATE discord_development_environments
		SET projects_scanned_at=now(), project_scan_error=NULL, updated_at=now()
		WHERE id=$1`, environmentID); err != nil {
		problem(c, http.StatusInternalServerError, "更新开发项目扫描时间失败", err)
		return
	}
	if err := tx.Commit(); err != nil {
		problem(c, http.StatusInternalServerError, "提交开发项目快照失败", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) workerDevelopmentEnvironments(c *gin.Context) {
	rows, err := s.db.QueryContext(c.Request.Context(), `SELECT id, container_name,
		COALESCE(container_id,''), COALESCE(image_ref,''), data_volume_name,
		home_volume_name, network_name, COALESCE(runtime_user,''), COALESCE(runtime_uid,0),
		COALESCE(runtime_gid,0), COALESCE(runtime_home,''), COALESCE(ssh_public_key,''),
		COALESCE(ssh_port,0), ssh_config_revision, ssh_applied_revision,
		COALESCE(e.ssh_discord_user_id, ''),
		COALESCE(NULLIF(m.display_name, ''), m.username, ''), e.guild_id
		FROM discord_development_environments e
		LEFT JOIN discord_members m ON m.guild_id = e.guild_id
			AND m.discord_user_id = e.ssh_discord_user_id
		WHERE execution_node_id = $1 AND e.status NOT IN ('deleting','pending','building')
		AND container_id IS NOT NULL ORDER BY created_at, id`, workerNode(c).ID)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取开发环境 Manifest 失败", err)
		return
	}
	defer func() { _ = rows.Close() }()
	environments := make([]workerprotocol.EnvironmentManifest, 0)
	for rows.Next() {
		var item workerprotocol.EnvironmentManifest
		var sshUserID, sshDisplayName, guildID string
		if err := rows.Scan(&item.EnvironmentID, &item.ContainerName, &item.ContainerID,
			&item.ImageRef, &item.DataVolume, &item.HomeVolume, &item.Network,
			&item.RuntimeUser, &item.RuntimeUID, &item.RuntimeGID, &item.RuntimeHome,
			&item.SSHPublicKey, &item.SSHPort, &item.SSHConfigRevision,
			&item.AppliedRevision, &sshUserID, &sshDisplayName, &guildID); err != nil {
			problem(c, http.StatusInternalServerError, "解析开发环境 Manifest 失败", err)
			return
		}
		if sshUserID != "" {
			item.SSHParticipant = &workerprotocol.ParticipantIdentity{
				ParticipantID: participantidentity.ID(guildID, sshUserID),
				DiscordUserID: sshUserID, DisplayName: sshDisplayName,
			}
		}
		environments = append(environments, item)
	}
	if err := rows.Close(); err != nil {
		problem(c, http.StatusInternalServerError, "读取开发环境 Manifest 失败", err)
		return
	}
	for index := range environments {
		forums, err := s.environmentManifestForums(c, environments[index].EnvironmentID)
		if err != nil {
			problem(c, http.StatusInternalServerError, "读取开发环境 Forum 失败", err)
			return
		}
		environments[index].Forums = forums
	}
	c.JSON(http.StatusOK, gin.H{"environments": environments})
}

func (s *Server) environmentManifestForums(c *gin.Context,
	environmentID uuid.UUID,
) ([]workerprotocol.EnvironmentForum, error) {
	rows, err := s.db.QueryContext(c.Request.Context(), `SELECT f.id, f.guild_id,
		r.discord_id, f.owner_discord_user_id, project.id::text,
		project.project_kind, project.relative_path, 'ready'
		FROM discord_forums f JOIN discord_resources r ON r.id = f.resource_id
		JOIN development_projects project ON project.id=f.development_project_id
		WHERE f.development_environment_id = $1
			AND f.binding_status='active'
			AND project.availability_status='available'
		ORDER BY project.relative_path, f.id`, environmentID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make([]workerprotocol.EnvironmentForum, 0)
	for rows.Next() {
		var item workerprotocol.EnvironmentForum
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

func (s *Server) workerEnvironmentDaemonState(c *gin.Context) {
	environmentID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	var request workerprotocol.EnvironmentDaemonState
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	if request.EnvironmentID != environmentID ||
		(request.Status != "starting" && request.Status != "running" && request.Status != "error") ||
		!validEnvironmentComponentState(request.AppServerStatus, false) ||
		!validEnvironmentComponentState(request.SSHStatus, true) ||
		!validEnvironmentComponentState(request.RelayStatus, false) {
		badRequest(c, errors.New("开发环境 daemon 状态无效"))
		return
	}
	result, err := s.db.ExecContext(c.Request.Context(), `UPDATE discord_development_environments
		SET daemon_status = $3, daemon_error = NULLIF($4,''), app_server_status=$5,
		ssh_daemon_status=$6, relay_status=$7, codex_version=NULLIF($8,''),
		codex_user_override=$9, updated_at = now()
		WHERE id = $1 AND execution_node_id = $2`, environmentID, workerNode(c).ID,
		request.Status, request.Error, request.AppServerStatus, request.SSHStatus,
		request.RelayStatus, request.CodexVersion, request.CodexUserOverride)
	if err != nil {
		problem(c, http.StatusInternalServerError, "保存开发环境 daemon 状态失败", err)
		return
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		problem(c, http.StatusNotFound, "开发环境不存在", sql.ErrNoRows)
		return
	}
	c.Status(http.StatusNoContent)
}

func validEnvironmentComponentState(value string, allowDisabled bool) bool {
	return value == "pending" || value == "starting" || value == "running" || value == "error" ||
		(allowDisabled && value == "disabled")
}

func (s *Server) workerEnvironmentRuntimeCredential(c *gin.Context) {
	environmentID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	var owned bool
	if err := s.db.QueryRowContext(c.Request.Context(), `SELECT EXISTS(
		SELECT 1 FROM discord_development_environments
		WHERE id = $1 AND execution_node_id = $2 AND status <> 'deleting')`, environmentID,
		workerNode(c).ID).Scan(&owned); err != nil || !owned {
		problem(c, http.StatusForbidden, "开发环境不属于当前执行节点", err)
		return
	}
	credential, err := s.codexRuntimeCredential(c.Request.Context())
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取开发环境 Provider 凭据失败", err)
		return
	}
	c.JSON(http.StatusOK, credential)
}
