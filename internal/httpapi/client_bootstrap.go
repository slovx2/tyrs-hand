package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"path"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/auth"
)

const (
	clientProtocolVersion = 4
	clientDeviceContext   = "clientDeviceID"
)

func (s *Server) clientControlInstanceID(ctx context.Context) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.db.QueryRowContext(ctx, `SELECT id FROM control_instances WHERE singleton=true`).Scan(&id)
	return id, err
}

func (s *Server) clientBootstrap(c *gin.Context) {
	session := c.MustGet("session").(auth.Session)
	serverID, err := s.clientControlInstanceID(c.Request.Context())
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Control 身份失败", err)
		return
	}
	type option struct {
		ID   uuid.UUID `json:"id"`
		Name string    `json:"name"`
	}
	workspaces := make([]option, 0)
	rows, err := s.db.QueryContext(c.Request.Context(), `SELECT workspace.id,
		COALESCE(NULLIF(member.display_name,''), member.username)
		FROM worker_workspaces workspace
		JOIN discord_members member ON member.guild_id=workspace.guild_id
			AND member.discord_user_id=workspace.owner_discord_user_id
		ORDER BY lower(COALESCE(NULLIF(member.display_name,''), member.username)), workspace.id`)
	if err == nil {
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var item option
			if err = rows.Scan(&item.ID, &item.Name); err != nil {
				break
			}
			workspaces = append(workspaces, item)
		}
	}
	projects := make([]struct {
		ID                 uuid.UUID `json:"id"`
		WorkspaceID        uuid.UUID `json:"workspaceId"`
		Name               string    `json:"name"`
		RelativePath       string    `json:"relativePath"`
		AbsolutePath       string    `json:"absolutePath"`
		Kind               string    `json:"kind"`
		AvailabilityStatus string    `json:"availabilityStatus"`
		Branch             *string   `json:"branch"`
		Dirty              bool      `json:"dirty"`
	}, 0)
	if err == nil {
		var projectRows *sql.Rows
		projectRows, err = s.db.QueryContext(c.Request.Context(), `SELECT project.id,
			project.workspace_id,project.name,project.relative_path,project.project_kind,
			project.availability_status,project.branch,project.dirty,
			COALESCE(worker.metadata->'host'->>'workspaceRoot','')
			FROM workspace_projects project
			JOIN worker_workspaces workspace ON workspace.id=project.workspace_id
			LEFT JOIN workers worker ON worker.id=workspace.worker_id
			ORDER BY lower(project.name),project.id`)
		if err == nil {
			defer func() { _ = projectRows.Close() }()
			for projectRows.Next() {
				var item struct {
					ID                 uuid.UUID `json:"id"`
					WorkspaceID        uuid.UUID `json:"workspaceId"`
					Name               string    `json:"name"`
					RelativePath       string    `json:"relativePath"`
					AbsolutePath       string    `json:"absolutePath"`
					Kind               string    `json:"kind"`
					AvailabilityStatus string    `json:"availabilityStatus"`
					Branch             *string   `json:"branch"`
					Dirty              bool      `json:"dirty"`
				}
				var branch sql.NullString
				var workspaceRoot string
				if err = projectRows.Scan(&item.ID, &item.WorkspaceID, &item.Name,
					&item.RelativePath, &item.Kind, &item.AvailabilityStatus, &branch,
					&item.Dirty, &workspaceRoot); err != nil {
					break
				}
				item.AbsolutePath, err = desktopWorkspacePath(workspaceRoot, item.RelativePath)
				if err != nil {
					break
				}
				if branch.Valid {
					item.Branch = &branch.String
				}
				projects = append(projects, item)
			}
		}
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取客户端启动数据失败", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"serverId": serverID, "protocolVersion": clientProtocolVersion,
		"user": gin.H{"id": session.AdministratorID, "username": session.Username},
		"capabilities": gin.H{"attachments": true, "pushNotifications": true,
			"appServerTunnel": true},
		"workspaces": workspaces, "projects": projects,
	})
}

func desktopWorkspacePath(root, relative string) (string, error) {
	root = path.Clean(strings.TrimSpace(root))
	clean := path.Clean(strings.TrimSpace(relative))
	parts := strings.Split(clean, "/")
	if root == "." || !path.IsAbs(root) || len(parts) != 2 || parts[0] != "workspaces" ||
		parts[1] == "" || parts[1] == "." || parts[1] == ".." {
		return "", errors.New("workspace 项目路径必须是绝对根目录下的 workspaces/<name>")
	}
	return path.Join(root, parts[1]), nil
}

func normalizeDesktopTitle(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > 100 {
		value = string(runes[:100])
	}
	return value
}

func parseOptionalUUID(value sql.NullString) uuid.UUID {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return uuid.Nil
	}
	parsed, _ := uuid.Parse(value.String)
	return parsed
}
