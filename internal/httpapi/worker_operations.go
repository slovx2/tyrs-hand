package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/security"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

func (s *Server) claimDevelopmentOperation(ctx context.Context, nodeID uuid.UUID,
	workerID string,
) (*workerprotocol.DevelopmentOperation, error) {
	leaseToken, err := security.RandomToken(32)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var result workerprotocol.DevelopmentOperation
	var projectID, imageRef, workspace, targetWorkspace sql.NullString
	var runtimeUser, runtimeHome, sshPublicKey sql.NullString
	var sshPort sql.NullInt64
	var previousEpoch int64
	err = tx.QueryRowContext(ctx, `SELECT o.id, o.operation, o.environment_id,
		o.development_project_id::text, o.lease_epoch, e.status, e.container_name, e.image_ref,
		COALESCE(e.image_id,''), COALESCE(e.container_id,''),
		e.data_volume_name, e.home_volume_name, e.network_name,
		project.relative_path, project.desired_relative_path,
		COALESCE(project.project_kind,''),
		e.runtime_user, COALESCE(e.runtime_uid,0), COALESCE(e.runtime_gid,0), e.runtime_home,
		e.ssh_public_key, e.ssh_port, e.ssh_config_revision
		FROM discord_development_operations o
		JOIN discord_development_environments e ON e.id = o.environment_id
		LEFT JOIN development_projects project ON project.id = o.development_project_id
		WHERE o.execution_node_id = $1 AND (
			o.status = 'pending' OR (o.status = 'running' AND o.lease_expires_at < now()))
		AND (o.operation NOT IN ('reconfigure','rebase') OR NOT EXISTS (
			SELECT 1 FROM codex_thread_controls ct JOIN codex_turn_runs r ON r.control_id = ct.id
			WHERE ct.development_environment_id = e.id
			AND r.status IN ('starting','running','waiting_for_user','reconciling')
		))
		ORDER BY o.created_at FOR UPDATE OF o SKIP LOCKED LIMIT 1`, nodeID).Scan(
		&result.ID, &result.Operation, &result.EnvironmentID, &projectID, &previousEpoch,
		&result.EnvironmentStatus, &result.ContainerName, &imageRef, &result.ImageID,
		&result.ContainerID, &result.DataVolume, &result.HomeVolume,
		&result.Network, &workspace, &targetWorkspace, &result.WorkspaceKind, &runtimeUser,
		&result.RuntimeUID, &result.RuntimeGID, &runtimeHome, &sshPublicKey, &sshPort,
		&result.SSHConfigRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result.ImageRef, result.Workspace = imageRef.String, workspace.String
	result.TargetWorkspace = targetWorkspace.String
	if result.Operation == "rebase" || result.Operation == "provision_environment" {
		result.ImageRef = s.cfg.DevelopmentImage
		if result.ImageRef == "" {
			return nil, errors.New("control 未配置 TYRS_HAND_DEVELOPMENT_IMAGE")
		}
	}
	result.RuntimeUser, result.RuntimeHome = runtimeUser.String, runtimeHome.String
	result.SSHPublicKey, result.SSHPort = sshPublicKey.String, int(sshPort.Int64)
	if projectID.Valid {
		parsed, parseErr := uuid.Parse(projectID.String)
		if parseErr != nil {
			return nil, parseErr
		}
		result.ProjectID = &parsed
	}
	result.LeaseToken, result.LeaseEpoch = leaseToken, previousEpoch+1
	_, err = tx.ExecContext(ctx, `UPDATE discord_development_operations SET
		status = 'running', worker_id = $2, lease_token = $3, lease_epoch = $4,
		lease_expires_at = now() + $5::interval, attempt_count = attempt_count + 1,
		started_at = COALESCE(started_at, now()), error = NULL, updated_at = now()
		WHERE id = $1`, result.ID, workerID, security.Digest(leaseToken), result.LeaseEpoch,
		s.cfg.LeaseDuration.String())
	if err != nil {
		return nil, err
	}
	switch result.Operation {
	case "reconfigure":
		if _, err = tx.ExecContext(ctx, `UPDATE discord_development_environments SET
			daemon_status = 'starting', daemon_error = NULL, updated_at = now() WHERE id = $1`,
			result.EnvironmentID); err != nil {
			return nil, err
		}
	case "provision_environment":
		if _, err = tx.ExecContext(ctx, `UPDATE discord_development_environments SET
			status='building', error=NULL, updated_at=now() WHERE id=$1`,
			result.EnvironmentID); err != nil {
			return nil, err
		}
	}
	return &result, tx.Commit()
}

func (s *Server) workerDevelopmentOperationHeartbeat(c *gin.Context) {
	id, ok := parseDevelopmentOperationID(c)
	if !ok {
		return
	}
	var request workerprotocol.DevelopmentOperationLease
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	result, err := s.db.ExecContext(c, `UPDATE discord_development_operations SET
		lease_expires_at = now() + $5::interval, updated_at = now()
		WHERE id = $1 AND execution_node_id = $2 AND status = 'running'
		AND lease_token = $3 AND lease_epoch = $4`, id, workerNode(c).ID,
		security.Digest(request.LeaseToken), request.LeaseEpoch, s.cfg.LeaseDuration.String())
	if err != nil {
		problem(c, http.StatusInternalServerError, "开发环境 Operation 续租失败", err)
		return
	}
	if count, _ := result.RowsAffected(); count != 1 {
		problem(c, http.StatusConflict, "开发环境 Operation Lease 已失效", nil)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) workerCompleteDevelopmentOperation(c *gin.Context) {
	s.finishDevelopmentOperation(c, true)
}

func (s *Server) workerFailDevelopmentOperation(c *gin.Context) {
	s.finishDevelopmentOperation(c, false)
}

func (s *Server) finishDevelopmentOperation(c *gin.Context, succeeded bool) {
	id, ok := parseDevelopmentOperationID(c)
	if !ok {
		return
	}
	var request workerprotocol.DevelopmentOperationTerminal
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	if request.IdempotencyKey == "" {
		badRequest(c, errors.New("缺少幂等键"))
		return
	}
	tx, err := s.db.BeginTx(c, nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "完成开发环境 Operation 失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var status, operation, leaseHash string
	var environmentID, projectID sql.NullString
	var leaseEpoch int64
	var terminalKey sql.NullString
	err = tx.QueryRowContext(c, `SELECT status, operation, environment_id::text,
		development_project_id::text, COALESCE(lease_token,''), lease_epoch, terminal_key
		FROM discord_development_operations WHERE id = $1 AND execution_node_id = $2
		FOR UPDATE`, id, workerNode(c).ID).Scan(&status, &operation, &environmentID,
		&projectID, &leaseHash, &leaseEpoch, &terminalKey)
	if err != nil {
		workerOperationError(c, err)
		return
	}
	if terminalKey.Valid && terminalKey.String == request.IdempotencyKey &&
		(status == "completed" || status == "failed") {
		c.Status(http.StatusNoContent)
		return
	}
	if status != "running" || leaseHash != security.Digest(request.LeaseToken) ||
		leaseEpoch != request.LeaseEpoch {
		problem(c, http.StatusConflict, "开发环境 Operation Lease 已失效", nil)
		return
	}
	if succeeded {
		err = completeDevelopmentOperation(c, tx, operation, environmentID, projectID, request)
	} else {
		err = failDevelopmentOperation(c, tx, operation, environmentID, projectID, request.Error)
	}
	if err == nil {
		terminalStatus := "completed"
		if !succeeded {
			terminalStatus = "failed"
		}
		_, err = tx.ExecContext(c, `UPDATE discord_development_operations SET status = $2,
			terminal_key = $3, error = NULLIF($4,''), lease_token = NULL,
			lease_expires_at = NULL, finished_at = now(), updated_at = now() WHERE id = $1`,
			id, terminalStatus, request.IdempotencyKey, request.Error)
	}
	if err == nil && succeeded && operation == "reconfigure" {
		_, err = tx.ExecContext(c, `INSERT INTO discord_development_operations
			(environment_id, operation, execution_node_id)
			SELECT id, 'reconfigure', execution_node_id
			FROM discord_development_environments
			WHERE id = $1 AND ssh_applied_revision < ssh_config_revision`, environmentID.String)
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "保存开发环境 Operation 结果失败", err)
		return
	}
	if err := tx.Commit(); err != nil {
		problem(c, http.StatusInternalServerError, "提交开发环境 Operation 结果失败", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func completeDevelopmentOperation(ctx context.Context, tx *sql.Tx, operation string,
	environmentID, projectID sql.NullString, request workerprotocol.DevelopmentOperationTerminal,
) error {
	switch operation {
	case "provision_environment":
		if request.ContainerID == "" || request.ImageID == "" ||
			request.RuntimeUser == "" || request.RuntimeUID <= 0 ||
			request.RuntimeGID <= 0 || request.RuntimeHome == "" ||
			request.DaemonStatus != "running" {
			return errors.New("worker 未返回有效的环境创建结果")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE discord_development_environments SET
			status = 'running', image_ref=$2, image_id = $3, container_id = $4,
			runtime_user = $5, runtime_uid = $6, runtime_gid = $7, runtime_home = $8,
			ssh_applied_revision = GREATEST(ssh_applied_revision, $9),
			daemon_status = 'running', app_server_status = 'running',
			relay_status = 'running',
			ssh_daemon_status = CASE WHEN ssh_public_key IS NULL THEN 'disabled' ELSE 'running' END,
			daemon_error = NULL, error = NULL, last_used_at = now(), updated_at = now()
			WHERE id = $1`, environmentID.String, request.ImageRef, request.ImageID,
			request.ContainerID,
			request.RuntimeUser, request.RuntimeUID, request.RuntimeGID,
			request.RuntimeHome, request.AppliedRevision); err != nil {
			return err
		}
		return nil
	case "relocate_project":
		if !projectID.Valid {
			return errors.New("项目迁移缺少项目 ID")
		}
		var targetPath string
		if err := tx.QueryRowContext(ctx, `SELECT desired_relative_path
			FROM development_projects WHERE id=$1 FOR UPDATE`, projectID.String).
			Scan(&targetPath); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM development_projects target
			WHERE target.environment_id=(SELECT environment_id FROM development_projects WHERE id=$1)
			  AND target.relative_path=$2 AND target.id<>$1
			  AND NOT EXISTS (SELECT 1 FROM discord_forums
				WHERE development_project_id=target.id)
			  AND NOT EXISTS (SELECT 1 FROM codex_thread_controls
				WHERE development_project_id=target.id)`, projectID.String, targetPath)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE development_projects
			SET relative_path=$2, desired_relative_path=NULL, availability_status='available',
				scan_error=NULL, updated_at=now() WHERE id=$1`,
			projectID.String, targetPath)
		return err
	case "reconfigure":
		if request.AppliedRevision <= 0 || request.ContainerID == "" || request.DaemonStatus != "running" {
			return errors.New("worker 未返回有效的 daemon 应用状态")
		}
		_, err := tx.ExecContext(ctx, `UPDATE discord_development_environments SET
			container_id = $2, ssh_applied_revision = $3, daemon_status = 'running',
			daemon_error = NULL, updated_at = now() WHERE id = $1
			AND ssh_config_revision >= $3 AND ssh_applied_revision < $3`, environmentID.String,
			request.ContainerID, request.AppliedRevision)
		return err
	case "rebase":
		if request.ContainerID == "" || request.ImageRef == "" || request.ImageID == "" ||
			request.DaemonStatus != "running" {
			return errors.New("worker 未返回有效的 Rebase 结果")
		}
		_, err := tx.ExecContext(ctx, `UPDATE discord_development_environments SET
			status = 'running', image_ref = $2, image_id = $3, container_id = $4,
			ssh_applied_revision = GREATEST(ssh_applied_revision, $5),
			daemon_status = 'running', daemon_error = NULL, error = NULL,
			updated_at = now() WHERE id = $1`, environmentID.String, request.ImageRef,
			request.ImageID, request.ContainerID, request.AppliedRevision)
		return err
	default:
		return errors.New("不支持的远程开发环境 Operation")
	}
}

func failDevelopmentOperation(ctx context.Context, tx *sql.Tx, operation string,
	environmentID, projectID sql.NullString, message string,
) error {
	if message == "" {
		message = "Worker 未提供失败原因"
	}
	if operation == "relocate_project" && projectID.Valid {
		_, err := tx.ExecContext(ctx, `UPDATE development_projects
			SET scan_error=$2, updated_at=now() WHERE id=$1`, projectID.String, message)
		return err
	}
	if environmentID.Valid {
		if operation == "reconfigure" {
			_, err := tx.ExecContext(ctx, `UPDATE discord_development_environments SET
				daemon_status = 'error', daemon_error = $2, updated_at = now() WHERE id = $1`,
				environmentID.String, message)
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE discord_development_environments SET
			status = 'error', error = $2, updated_at = now() WHERE id = $1`,
			environmentID.String, message); err != nil {
			return err
		}
	}
	return nil
}

func parseDevelopmentOperationID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return uuid.Nil, false
	}
	return id, true
}

func workerOperationError(c *gin.Context, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusNotFound, "开发环境 Operation 不存在", err)
		return
	}
	problem(c, http.StatusInternalServerError, "读取开发环境 Operation 失败", err)
}
