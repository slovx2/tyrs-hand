package devcontainer

import (
	"context"
	"database/sql"
	"encoding/json"
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
	var forumID sql.NullString
	var operation string
	err = tx.QueryRowContext(ctx, `SELECT id, environment_id, forum_id::text, operation
		FROM discord_development_operations
		WHERE status = 'pending' ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1`).
		Scan(&operationID, &environmentID, &forumID, &operation)
	if errors.Is(err, sql.ErrNoRows) {
		return
	}
	if err != nil {
		return
	}
	if _, err := tx.ExecContext(ctx, `UPDATE discord_development_operations SET status = 'running',
		attempt_count = attempt_count + 1, started_at = COALESCE(started_at, now()), updated_at = now()
		WHERE id = $1`, operationID); err != nil || tx.Commit() != nil {
		return
	}
	if operation != "delete_forum" && operation != "delete_environment" {
		m.failOperation(operationID, errors.New("不支持脱离 Agent Run 的开发环境操作"))
		return
	}
	parsedForum, err := uuid.Parse(forumID.String)
	if err == nil {
		err = m.deleteForum(ctx, environmentID, parsedForum, operation == "delete_environment")
	}
	if err != nil {
		m.failOperation(operationID, err)
	}
}

func (m *Manager) deleteForum(ctx context.Context, environmentID, forumID uuid.UUID,
	deleteEnvironment bool,
) error {
	var container, dataVolume, homeVolume, network, imageRef, relative, channelID, resourceID string
	err := m.db.QueryRowContext(ctx, `SELECT e.container_name, e.data_volume_name, e.home_volume_name,
		e.network_name, COALESCE(e.image_ref, ''), fw.relative_path, dr.discord_id, dr.id::text
		FROM discord_development_environments e
		JOIN discord_forum_workspaces fw ON fw.environment_id = e.id
		JOIN discord_forums f ON f.id = fw.forum_id
		JOIN discord_resources dr ON dr.id = f.resource_id
		WHERE e.id = $1 AND fw.forum_id = $2`, environmentID, forumID).
		Scan(&container, &dataVolume, &homeVolume, &network, &imageRef, &relative, &channelID, &resourceID)
	if err != nil {
		return err
	}
	_, _ = m.docker(ctx, "start", container)
	_, _ = m.docker(ctx, "exec", "--user", "0:0", container, "rm", "-rf", containerRoot+"/"+relative)
	rows, _ := m.db.QueryContext(ctx, `SELECT id::text FROM discord_conversations WHERE forum_id = $1`, forumID)
	if rows != nil {
		for rows.Next() {
			var conversation string
			if rows.Scan(&conversation) == nil {
				_, _ = m.docker(ctx, "exec", "--user", "0:0", container, "rm", "-rf",
					containerRoot+"/codex/"+conversation)
			}
		}
		_ = rows.Close()
	}
	payload, _ := json.Marshal(map[string]string{"channelId": channelID})
	_, err = m.db.ExecContext(ctx, `INSERT INTO integration_outbox
		(integration, operation_key, operation_type, route_key, payload)
		VALUES ('discord', $1, 'channel.delete', $2, $3)
		ON CONFLICT(integration, operation_key) DO NOTHING`, "development-forum-delete:"+forumID.String(),
		"channels/"+channelID, payload)
	if err != nil {
		return err
	}
	if deleteEnvironment {
		_, _ = m.docker(ctx, "rm", "--force", container)
		_, _ = m.docker(ctx, "volume", "rm", dataVolume)
		_, _ = m.docker(ctx, "volume", "rm", homeVolume)
		_, _ = m.docker(ctx, "network", "rm", network)
	}
	if _, err := m.db.ExecContext(ctx, `DELETE FROM discord_resources WHERE id = $1`, resourceID); err != nil {
		return err
	}
	if deleteEnvironment {
		_, err = m.db.ExecContext(ctx, `DELETE FROM discord_development_environments WHERE id = $1`, environmentID)
	}
	return err
}

func (m *Manager) failOperation(id uuid.UUID, cause error) {
	_, _ = m.db.ExecContext(context.Background(), `UPDATE discord_development_operations
		SET status = 'failed', error = $2, finished_at = now(), updated_at = now() WHERE id = $1`, id, cause.Error())
	m.logger.Warn("开发环境维护操作失败", zap.String("operation_id", id.String()), zap.Error(cause))
}
