package devcontainer

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

func (m *Manager) processOperation(ctx context.Context) {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback() }()
	var operationID, environmentID uuid.UUID
	var projectID sql.NullString
	var operation RemoteOperation
	err = tx.QueryRowContext(ctx, `SELECT o.id, o.environment_id,
		o.development_project_id::text, o.operation,
		e.container_name, COALESCE(e.container_id,''), COALESCE(e.image_ref,''),
		COALESCE(e.image_id,''), e.data_volume_name, e.home_volume_name, e.network_name,
		COALESCE(project.relative_path,''), COALESCE(project.desired_relative_path,''),
		COALESCE(e.runtime_user,''), COALESCE(e.runtime_uid,0),
		COALESCE(e.runtime_gid,0), COALESCE(e.runtime_home,''),
		COALESCE(e.ssh_public_key,''), COALESCE(e.ssh_port,0), e.ssh_config_revision
		FROM discord_development_operations o
		JOIN discord_development_environments e ON e.id=o.environment_id
		LEFT JOIN development_projects project ON project.id=o.development_project_id
		WHERE o.status='pending'
		ORDER BY o.created_at FOR UPDATE OF o SKIP LOCKED LIMIT 1`).
		Scan(&operationID, &environmentID, &projectID, &operation.Operation,
			&operation.ContainerName, &operation.ContainerID, &operation.ImageRef,
			&operation.ImageID, &operation.DataVolume, &operation.HomeVolume,
			&operation.Network, &operation.Workspace, &operation.TargetWorkspace,
			&operation.RuntimeUser, &operation.RuntimeUID, &operation.RuntimeGID,
			&operation.RuntimeHome, &operation.SSHPublicKey, &operation.SSHPort,
			&operation.SSHConfigRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return
	}
	if err != nil {
		return
	}
	operation.EnvironmentID = environmentID
	if operation.Operation == "provision_environment" || operation.Operation == "rebase" {
		operation.ImageRef = m.developmentImage
	}
	if _, err := tx.ExecContext(ctx, `UPDATE discord_development_operations SET
		status='running', attempt_count=attempt_count+1,
		started_at=COALESCE(started_at,now()), updated_at=now()
		WHERE id=$1`, operationID); err != nil || tx.Commit() != nil {
		return
	}

	err = m.executeLocalOperation(ctx, operation, projectID)
	if err != nil {
		m.failOperation(operationID, environmentID, projectID, operation.Operation, err)
		return
	}
	_, err = m.db.ExecContext(ctx, `UPDATE discord_development_operations SET
		status='completed', error=NULL, finished_at=now(), updated_at=now()
		WHERE id=$1`, operationID)
	if err != nil {
		m.logger.Warn("完成开发环境维护操作失败",
			zap.String("operation_id", operationID.String()), zap.Error(err))
	}
}

func (m *Manager) executeLocalOperation(ctx context.Context, operation RemoteOperation,
	projectID sql.NullString,
) error {
	switch operation.Operation {
	case "provision_environment":
		item := workspace{Environment: environment{
			ID: operation.EnvironmentID, Status: "pending", ImageRef: operation.ImageRef,
			ImageID: operation.ImageID, ContainerName: operation.ContainerName,
			ContainerID: operation.ContainerID, DataVolume: operation.DataVolume,
			HomeVolume: operation.HomeVolume, Network: operation.Network,
			RuntimeUser: operation.RuntimeUser, RuntimeUID: operation.RuntimeUID,
			RuntimeGID: operation.RuntimeGID, RuntimeHome: operation.RuntimeHome,
		}}
		if err := m.provision(ctx, &item, "", nil); err != nil {
			return err
		}
		_, err := m.db.ExecContext(ctx, `UPDATE discord_development_environments SET
			daemon_status='running', app_server_status='running', relay_status='running',
			ssh_daemon_status=CASE WHEN ssh_public_key IS NULL THEN 'disabled' ELSE 'running' END,
			ssh_applied_revision=ssh_config_revision, daemon_error=NULL, updated_at=now()
			WHERE id=$1`, operation.EnvironmentID)
		return err
	case "relocate_project":
		if !projectID.Valid {
			return errors.New("项目迁移缺少项目 ID")
		}
		if err := m.RelocateRemoteProject(ctx, operation); err != nil {
			return err
		}
		return m.completeLocalProjectRelocation(ctx, projectID.String)
	case "reconfigure", "rebase":
		if err := m.RunRemoteOperation(ctx, operation); err != nil {
			return err
		}
		containerID, err := m.ContainerID(ctx, operation.ContainerName)
		if err != nil {
			return err
		}
		imageID := operation.ImageID
		if operation.Operation == "rebase" {
			imageID, err = m.ImageID(ctx, operation.ImageRef)
			if err != nil {
				return err
			}
		}
		_, err = m.db.ExecContext(ctx, `UPDATE discord_development_environments SET
			status='running', container_id=$2, image_ref=COALESCE(NULLIF($3,''),image_ref),
			image_id=COALESCE(NULLIF($4,''),image_id),
			ssh_applied_revision=ssh_config_revision, daemon_status='running',
			daemon_error=NULL, error=NULL, updated_at=now() WHERE id=$1`,
			operation.EnvironmentID, containerID, operation.ImageRef, imageID)
		return err
	default:
		return errors.New("不支持的开发环境维护操作")
	}
}

func (m *Manager) completeLocalProjectRelocation(ctx context.Context, projectID string) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var targetPath string
	if err := tx.QueryRowContext(ctx, `SELECT desired_relative_path
		FROM development_projects WHERE id=$1 FOR UPDATE`, projectID).
		Scan(&targetPath); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM development_projects target
		WHERE target.environment_id=(SELECT environment_id FROM development_projects WHERE id=$1)
		  AND target.relative_path=$2 AND target.id<>$1
		  AND NOT EXISTS (SELECT 1 FROM discord_forums
			WHERE development_project_id=target.id)
		  AND NOT EXISTS (SELECT 1 FROM codex_thread_controls
			WHERE development_project_id=target.id)`, projectID, targetPath); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE development_projects
		SET relative_path=$2, desired_relative_path=NULL,
			availability_status='available', scan_error=NULL, updated_at=now()
		WHERE id=$1`, projectID, targetPath); err != nil {
		return err
	}
	return tx.Commit()
}

func (m *Manager) failOperation(id, environmentID uuid.UUID, projectID sql.NullString,
	operation string, cause error,
) {
	_, _ = m.db.ExecContext(context.Background(), `UPDATE discord_development_operations
		SET status='failed', error=$2, finished_at=now(), updated_at=now() WHERE id=$1`,
		id, cause.Error())
	if operation == "relocate_project" && projectID.Valid {
		_, _ = m.db.ExecContext(context.Background(), `UPDATE development_projects
			SET scan_error=$2, updated_at=now() WHERE id=$1`, projectID.String, cause.Error())
	} else {
		_, _ = m.db.ExecContext(context.Background(), `UPDATE discord_development_environments
			SET status='error', error=$2, updated_at=now() WHERE id=$1`,
			environmentID, cause.Error())
	}
	m.logger.Warn("开发环境维护操作失败",
		zap.String("operation_id", id.String()), zap.Error(cause))
}
