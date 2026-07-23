package httpapi

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/participantidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

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
		r.discord_id, f.owner_discord_user_id, f.repository_id, fw.relative_path, fw.status
		FROM discord_forums f JOIN discord_resources r ON r.id = f.resource_id
		JOIN discord_forum_workspaces fw ON fw.forum_id = f.id
		WHERE f.development_environment_id = $1 AND fw.status <> 'deleting'
		ORDER BY fw.relative_path, f.id`, environmentID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make([]workerprotocol.EnvironmentForum, 0)
	for rows.Next() {
		var item workerprotocol.EnvironmentForum
		if err := rows.Scan(&item.ForumID, &item.GuildID, &item.DiscordForumID,
			&item.OwnerUserID, &item.RepositoryID, &item.WorkspaceRelative,
			&item.WorkspaceStatus); err != nil {
			return nil, err
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
		ssh_daemon_status=$6, relay_status=$7, updated_at = now()
		WHERE id = $1 AND execution_node_id = $2`, environmentID, workerNode(c).ID,
		request.Status, request.Error, request.AppServerStatus, request.SSHStatus,
		request.RelayStatus)
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
	provider, err := s.settings.AgentProvider(c.Request.Context())
	if err != nil || provider.ProviderType != "api-key" {
		problem(c, http.StatusConflict, "开发环境只支持 API Key Provider", err)
		return
	}
	key, err := s.settings.APIKey(c.Request.Context())
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取开发环境 Provider 凭据失败", err)
		return
	}
	agents, err := s.settings.GlobalAgents(c.Request.Context())
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取全局 AGENTS.md 失败", err)
		return
	}
	c.JSON(http.StatusOK, workerprotocol.RuntimeCredential{APIKey: string(key),
		BaseURL: provider.BaseURL, ProxyURL: provider.ProxyURL,
		ConfigSignature: provider.ConfigSignature, GlobalAgents: agents.Content})
}
