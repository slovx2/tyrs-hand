package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
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
	var forumID, imageRef, workspace, runtimeUser, runtimeHome, sshPublicKey sql.NullString
	var sshPort sql.NullInt64
	var previousEpoch int64
	err = tx.QueryRowContext(ctx, `SELECT o.id, o.operation, o.environment_id,
		o.forum_id::text, o.lease_epoch, e.container_name, e.image_ref,
		e.data_volume_name, e.home_volume_name, e.network_name, fw.relative_path,
		e.runtime_user, COALESCE(e.runtime_uid,0), COALESCE(e.runtime_gid,0), e.runtime_home,
		e.ssh_public_key, e.ssh_port, e.ssh_config_revision
		FROM discord_development_operations o
		JOIN discord_development_environments e ON e.id = o.environment_id
		LEFT JOIN discord_forum_workspaces fw ON fw.forum_id = o.forum_id
		WHERE o.execution_node_id = $1 AND (
			o.status = 'pending' OR (o.status = 'running' AND o.lease_expires_at < now()))
		AND (o.operation <> 'reconfigure' OR NOT EXISTS (
			SELECT 1 FROM codex_thread_controls ct JOIN codex_turn_runs r ON r.control_id = ct.id
			WHERE ct.development_environment_id = e.id
			AND r.status IN ('starting','running','waiting_for_user','reconciling')
		))
		ORDER BY o.created_at FOR UPDATE OF o SKIP LOCKED LIMIT 1`, nodeID).Scan(
		&result.ID, &result.Operation, &result.EnvironmentID, &forumID, &previousEpoch,
		&result.ContainerName, &imageRef, &result.DataVolume, &result.HomeVolume,
		&result.Network, &workspace, &runtimeUser, &result.RuntimeUID, &result.RuntimeGID,
		&runtimeHome, &sshPublicKey, &sshPort, &result.SSHConfigRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result.ImageRef, result.Workspace = imageRef.String, workspace.String
	result.RuntimeUser, result.RuntimeHome = runtimeUser.String, runtimeHome.String
	result.SSHPublicKey, result.SSHPort = sshPublicKey.String, int(sshPort.Int64)
	if forumID.Valid {
		parsed, parseErr := uuid.Parse(forumID.String)
		if parseErr != nil {
			return nil, parseErr
		}
		result.ForumID = &parsed
		rows, queryErr := tx.QueryContext(ctx,
			`SELECT id FROM discord_conversations WHERE forum_id = $1 ORDER BY created_at`, parsed)
		if queryErr != nil {
			return nil, queryErr
		}
		for rows.Next() {
			var conversationID uuid.UUID
			if scanErr := rows.Scan(&conversationID); scanErr != nil {
				_ = rows.Close()
				return nil, scanErr
			}
			result.ConversationIDs = append(result.ConversationIDs, conversationID)
		}
		if rowsErr := rows.Close(); rowsErr != nil {
			return nil, rowsErr
		}
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
	if result.Operation == "reconfigure" {
		if _, err = tx.ExecContext(ctx, `UPDATE discord_development_environments SET
			daemon_status = 'starting', daemon_error = NULL, updated_at = now() WHERE id = $1`,
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
	var environmentID, forumID sql.NullString
	var leaseEpoch int64
	var terminalKey sql.NullString
	err = tx.QueryRowContext(c, `SELECT status, operation, environment_id::text,
		forum_id::text, COALESCE(lease_token,''), lease_epoch, terminal_key
		FROM discord_development_operations WHERE id = $1 AND execution_node_id = $2
		FOR UPDATE`, id, workerNode(c).ID).Scan(&status, &operation, &environmentID,
		&forumID, &leaseHash, &leaseEpoch, &terminalKey)
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
		err = completeDevelopmentOperation(c, tx, operation, environmentID, forumID, request)
	} else {
		err = failDevelopmentOperation(c, tx, operation, environmentID, forumID, request.Error)
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
	environmentID, forumID sql.NullString, request workerprotocol.DevelopmentOperationTerminal,
) error {
	switch operation {
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
	case "rebuild":
		_, err := tx.ExecContext(ctx, `UPDATE discord_development_environments SET
			status = 'pending', image_ref = NULL, image_id = NULL, container_id = NULL,
			runtime_user = NULL, runtime_uid = NULL, runtime_gid = NULL, runtime_home = NULL,
			build_source_sha = NULL, error = NULL, updated_at = now() WHERE id = $1`,
			environmentID.String)
		return err
	case "delete_forum", "delete_environment":
		return finalizeDevelopmentDeletion(ctx, tx, environmentID.String, forumID.String,
			operation == "delete_environment")
	default:
		return errors.New("不支持的远程开发环境 Operation")
	}
}

func finalizeDevelopmentDeletion(ctx context.Context, tx *sql.Tx, environmentID, forumID string,
	deleteEnvironment bool,
) error {
	var resourceID uuid.UUID
	var channelID string
	if err := tx.QueryRowContext(ctx, `SELECT dr.id, dr.discord_id FROM discord_forums f
		JOIN discord_resources dr ON dr.id = f.resource_id WHERE f.id = $1`, forumID).
		Scan(&resourceID, &channelID); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"channelId": channelID})
	if _, err := tx.ExecContext(ctx, `INSERT INTO integration_outbox
		(integration, operation_key, operation_type, route_key, payload)
		VALUES ('discord', $1, 'channel.delete', $2, $3)
		ON CONFLICT(integration, operation_key) DO NOTHING`,
		"development-forum-delete:"+forumID, "channels/"+channelID, payload); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM discord_resources WHERE id = $1`, resourceID); err != nil {
		return err
	}
	if deleteEnvironment {
		_, err := tx.ExecContext(ctx, `DELETE FROM discord_development_environments WHERE id = $1`,
			environmentID)
		return err
	}
	return nil
}

func failDevelopmentOperation(ctx context.Context, tx *sql.Tx, operation string,
	environmentID, forumID sql.NullString, message string,
) error {
	if message == "" {
		message = "Worker 未提供失败原因"
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
	if forumID.Valid {
		_, err := tx.ExecContext(ctx, `UPDATE discord_forum_workspaces SET
			status = 'error', error = $2, updated_at = now() WHERE forum_id = $1`,
			forumID.String, message)
		return err
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
