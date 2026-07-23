package executionnode

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/security"
)

const (
	ProtocolVersion       = 3
	GitHubDefaultSetting  = "execution.default.github"
	DiscordDefaultSetting = "execution.default.discord"
)

var (
	ErrUnauthorized = errors.New("执行节点凭据无效")
	ErrDisabled     = errors.New("执行节点已经禁用")
	ErrIncompatible = errors.New("执行节点协议版本不兼容")
)

type Node struct {
	ID                uuid.UUID       `json:"id"`
	Name              string          `json:"name"`
	Roles             []string        `json:"roles"`
	Enabled           bool            `json:"enabled"`
	MaxConcurrentJobs int             `json:"maxConcurrentJobs"`
	ProtocolVersion   int             `json:"protocolVersion"`
	WorkerVersion     string          `json:"workerVersion,omitempty"`
	Status            string          `json:"status"`
	HeartbeatAt       *time.Time      `json:"heartbeatAt,omitempty"`
	LastError         string          `json:"lastError,omitempty"`
	Metadata          json.RawMessage `json:"metadata"`
}

type Defaults struct {
	GitHubNodeID  *uuid.UUID `json:"githubNodeId"`
	DiscordNodeID *uuid.UUID `json:"discordNodeId"`
}

type Service struct {
	db *sql.DB
}

func NewService(db *sql.DB) *Service { return &Service{db: db} }

func normalizeRoles(roles []string) ([]string, error) {
	result := make([]string, 0, len(roles))
	for _, role := range roles {
		role = strings.TrimSpace(role)
		if role != "github" && role != "discord" {
			return nil, fmt.Errorf("不支持的执行节点角色 %q", role)
		}
		if !slices.Contains(result, role) {
			result = append(result, role)
		}
	}
	if len(result) == 0 {
		return nil, errors.New("执行节点至少需要一个角色")
	}
	return result, nil
}

func (s *Service) Create(ctx context.Context, name string, roles []string, maxJobs int) (Node, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 128 {
		return Node{}, "", errors.New("执行节点名称必须为 1 到 128 个字符")
	}
	roles, err := normalizeRoles(roles)
	if err != nil {
		return Node{}, "", err
	}
	if maxJobs <= 0 {
		maxJobs = 6
	}
	encoded, _ := json.Marshal(roles)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Node{}, "", err
	}
	defer func() { _ = tx.Rollback() }()
	var node Node
	node.Roles, node.Metadata = roles, json.RawMessage(`{}`)
	err = tx.QueryRowContext(ctx, `INSERT INTO execution_nodes
		(name, roles, max_concurrent_jobs, protocol_version)
		VALUES ($1,$2,$3,$4)
		RETURNING id, name, enabled, max_concurrent_jobs, protocol_version, status`,
		name, encoded, maxJobs, ProtocolVersion).Scan(&node.ID, &node.Name, &node.Enabled,
		&node.MaxConcurrentJobs, &node.ProtocolVersion, &node.Status)
	if err != nil {
		return Node{}, "", err
	}
	token, err := security.RandomToken(32)
	if err != nil {
		return Node{}, "", err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO execution_node_enrollments
		(node_id, token_hash, expires_at) VALUES ($1,$2,now() + interval '15 minutes')`,
		node.ID, security.Digest(token))
	if err != nil {
		return Node{}, "", err
	}
	return node, token, tx.Commit()
}

func (s *Service) NewEnrollment(ctx context.Context, nodeID uuid.UUID) (string, error) {
	var enabled bool
	if err := s.db.QueryRowContext(ctx, `SELECT enabled FROM execution_nodes WHERE id = $1`, nodeID).Scan(&enabled); err != nil {
		return "", err
	}
	if !enabled {
		return "", ErrDisabled
	}
	token, err := security.RandomToken(32)
	if err != nil {
		return "", err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO execution_node_enrollments
		(node_id, token_hash, expires_at) VALUES ($1,$2,now() + interval '15 minutes')`,
		nodeID, security.Digest(token))
	return token, err
}

func (s *Service) Enroll(ctx context.Context, token string) (Node, string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Node{}, "", err
	}
	defer func() { _ = tx.Rollback() }()
	var nodeID uuid.UUID
	err = tx.QueryRowContext(ctx, `UPDATE execution_node_enrollments SET consumed_at = now()
		WHERE token_hash = $1 AND consumed_at IS NULL AND expires_at > now()
		RETURNING node_id`, security.Digest(token)).Scan(&nodeID)
	if errors.Is(err, sql.ErrNoRows) {
		return Node{}, "", ErrUnauthorized
	}
	if err != nil {
		return Node{}, "", err
	}
	credential, err := security.RandomToken(32)
	if err != nil {
		return Node{}, "", err
	}
	updated, err := tx.ExecContext(ctx, `UPDATE execution_nodes SET credential_hash = $2,
		credential_version = credential_version + 1, status = 'offline', last_error = NULL,
		updated_at = now() WHERE id = $1 AND enabled`, nodeID, security.Digest(credential))
	if err != nil {
		return Node{}, "", err
	}
	if count, _ := updated.RowsAffected(); count != 1 {
		return Node{}, "", ErrDisabled
	}
	node, err := scanNode(tx.QueryRowContext(ctx, nodeSelect+" WHERE id = $1", nodeID))
	if err != nil {
		return Node{}, "", err
	}
	return node, credential, tx.Commit()
}

const nodeSelect = `SELECT id, name, roles, enabled, max_concurrent_jobs, protocol_version,
	COALESCE(worker_version,''), status, heartbeat_at, COALESCE(last_error,''), metadata
	FROM execution_nodes`

type scanner interface{ Scan(...any) error }

func scanNode(row scanner) (Node, error) {
	var node Node
	var roles []byte
	var heartbeat sql.NullTime
	err := row.Scan(&node.ID, &node.Name, &roles, &node.Enabled, &node.MaxConcurrentJobs,
		&node.ProtocolVersion, &node.WorkerVersion, &node.Status, &heartbeat, &node.LastError,
		&node.Metadata)
	if err != nil {
		return Node{}, err
	}
	if err := json.Unmarshal(roles, &node.Roles); err != nil {
		return Node{}, err
	}
	if heartbeat.Valid {
		node.HeartbeatAt = &heartbeat.Time
	}
	return node, nil
}

func (s *Service) Authenticate(ctx context.Context, credential string) (Node, error) {
	if credential == "" {
		return Node{}, ErrUnauthorized
	}
	node, err := scanNode(s.db.QueryRowContext(ctx, nodeSelect+" WHERE credential_hash = $1",
		security.Digest(credential)))
	if errors.Is(err, sql.ErrNoRows) {
		return Node{}, ErrUnauthorized
	}
	if err != nil {
		return Node{}, err
	}
	if !node.Enabled {
		return Node{}, ErrDisabled
	}
	if node.ProtocolVersion != ProtocolVersion {
		return Node{}, ErrIncompatible
	}
	return node, nil
}

func (s *Service) Heartbeat(ctx context.Context, id uuid.UUID, version string,
	protocolVersion int, metadata json.RawMessage,
) error {
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}
	status, lastError := "online", ""
	if protocolVersion != ProtocolVersion {
		status = "incompatible"
		lastError = fmt.Sprintf("Worker 协议版本 %d，Control 要求 %d", protocolVersion,
			ProtocolVersion)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE execution_nodes SET worker_version = $2,
		metadata = $3, status = $4, heartbeat_at = now(), last_error = NULLIF($5,''),
		updated_at = now() WHERE id = $1 AND enabled`, id, strings.TrimSpace(version), metadata,
		status, lastError)
	return err
}

func (s *Service) List(ctx context.Context) ([]Node, error) {
	rows, err := s.db.QueryContext(ctx, nodeSelect+" ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []Node
	for rows.Next() {
		node, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, node)
		if node.Status == "online" && node.HeartbeatAt != nil &&
			time.Since(*node.HeartbeatAt) > 2*time.Minute {
			result[len(result)-1].Status = "offline"
		}
	}
	return result, rows.Err()
}

func (s *Service) SetEnabled(ctx context.Context, id uuid.UUID, enabled bool) error {
	status := "disabled"
	if enabled {
		status = "offline"
	}
	result, err := s.db.ExecContext(ctx, `UPDATE execution_nodes SET enabled = $2,
		status = $3, updated_at = now() WHERE id = $1`, id, enabled, status)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM execution_nodes n WHERE n.id = $1
		AND NOT EXISTS (SELECT 1 FROM work_items WHERE execution_node_id = n.id)
		AND NOT EXISTS (SELECT 1 FROM discord_development_environments WHERE execution_node_id = n.id)
		AND NOT EXISTS (SELECT 1 FROM codex_thread_controls WHERE execution_node_id = n.id)
		AND NOT EXISTS (SELECT 1 FROM platform_settings WHERE setting_key IN ($2,$3)
			AND value->>'nodeId' = n.id::text)`, id, GitHubDefaultSetting, DiscordDefaultSetting)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("执行节点不存在或仍被资源引用")
	}
	return nil
}

func (s *Service) Defaults(ctx context.Context) (Defaults, error) {
	var result Defaults
	rows, err := s.db.QueryContext(ctx, `SELECT setting_key, value->>'nodeId'
		FROM platform_settings WHERE setting_key IN ($1,$2)`, GitHubDefaultSetting, DiscordDefaultSetting)
	if err != nil {
		return result, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var key string
		var raw sql.NullString
		if err := rows.Scan(&key, &raw); err != nil {
			return Defaults{}, err
		}
		if !raw.Valid || raw.String == "" {
			continue
		}
		id, err := uuid.Parse(raw.String)
		if err != nil {
			return Defaults{}, err
		}
		if key == GitHubDefaultSetting {
			result.GitHubNodeID = &id
		} else {
			result.DiscordNodeID = &id
		}
	}
	return result, rows.Err()
}

func (s *Service) SetDefaults(ctx context.Context, defaults Defaults) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, item := range []struct {
		key  string
		role string
		id   *uuid.UUID
	}{{GitHubDefaultSetting, "github", defaults.GitHubNodeID},
		{DiscordDefaultSetting, "discord", defaults.DiscordNodeID}} {
		if item.id == nil {
			if _, err := tx.ExecContext(ctx, `DELETE FROM platform_settings WHERE setting_key = $1`, item.key); err != nil {
				return err
			}
			continue
		}
		var valid bool
		if err := tx.QueryRowContext(ctx, `SELECT enabled AND roles ? $2 FROM execution_nodes WHERE id = $1`,
			*item.id, item.role).Scan(&valid); err != nil {
			return err
		}
		if !valid {
			return fmt.Errorf("节点 %s 未启用或不支持 %s 角色", item.id.String(), item.role)
		}
		value, _ := json.Marshal(map[string]string{"nodeId": item.id.String()})
		if _, err := tx.ExecContext(ctx, `INSERT INTO platform_settings(setting_key, value)
			VALUES ($1,$2) ON CONFLICT(setting_key) DO UPDATE SET value = EXCLUDED.value,
			version = platform_settings.version + 1, updated_at = now()`, item.key, value); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE work_items w
		SET execution_node_id = (setting.value->>'nodeId')::uuid, updated_at = now()
		FROM platform_settings setting
		WHERE setting.setting_key = $1 AND w.execution_node_id IS NULL AND EXISTS (
			SELECT 1 FROM codex_thread_controls c JOIN codex_turn_intents i ON i.control_id = c.id
			WHERE c.work_item_id = w.id AND i.status = 'placement_pending')`, GitHubDefaultSetting)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE codex_thread_controls c
		SET execution_node_id = w.execution_node_id, updated_at = now()
		FROM work_items w WHERE c.work_item_id = w.id AND c.execution_node_id IS NULL
		AND w.execution_node_id IS NOT NULL`)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE codex_turn_intents i SET status = 'queued', updated_at = now()
		FROM codex_thread_controls c WHERE i.control_id = c.id AND i.status = 'placement_pending'
		AND c.execution_node_id IS NOT NULL`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func HasRole(node Node, role string) bool { return slices.Contains(node.Roles, role) }
