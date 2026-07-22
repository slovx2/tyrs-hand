package sshconfig

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

var aliasPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func normalizeHost(input HostInput) (HostInput, error) {
	input.Alias = strings.TrimSpace(input.Alias)
	input.Hostname = strings.TrimSpace(input.Hostname)
	input.Username = strings.TrimSpace(input.Username)
	if !aliasPattern.MatchString(input.Alias) || len(input.Alias) > 128 {
		return input, errors.New("主机别名只能包含字母、数字、点、下划线和连字符")
	}
	if input.Hostname == "" || len(input.Hostname) > 255 || input.Username == "" || len(input.Username) > 128 {
		return input, errors.New("HostName 和用户名不能为空")
	}
	if strings.ContainsAny(input.Hostname, " \t\r\n") || strings.ContainsAny(input.Username, " \t\r\n") {
		return input, errors.New("HostName 和用户名不能包含空白字符")
	}
	if input.Port == 0 {
		input.Port = 22
	}
	if input.Port < 1 || input.Port > 65535 {
		return input, errors.New("SSH 端口必须在 1 到 65535 之间")
	}
	seen := make(map[uuid.UUID]bool)
	nodes := input.ExecutionNodeIDs[:0]
	for _, id := range input.ExecutionNodeIDs {
		if id != uuid.Nil && !seen[id] {
			seen[id] = true
			nodes = append(nodes, id)
		}
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].String() < nodes[j].String() })
	input.ExecutionNodeIDs = nodes
	return input, nil
}

func (s *Service) ListHosts(ctx context.Context) ([]Host, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT h.id, h.alias, h.hostname, h.port, h.username,
		h.credential_id, c.name, h.proxy_jump_host_id, COALESCE(p.alias,''), h.enabled,
		h.updated_at, COALESCE(array_agg(n.execution_node_id::text ORDER BY n.execution_node_id)
			FILTER (WHERE n.execution_node_id IS NOT NULL), '{}')
		FROM ssh_hosts h JOIN ssh_credentials c ON c.id = h.credential_id
		LEFT JOIN ssh_hosts p ON p.id = h.proxy_jump_host_id
		LEFT JOIN ssh_host_execution_nodes n ON n.host_id = h.id
		GROUP BY h.id, c.name, p.alias ORDER BY h.alias`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []Host
	for rows.Next() {
		item, err := scanHost(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

type rowScanner interface{ Scan(...any) error }

func scanHost(row rowScanner) (Host, error) {
	var item Host
	var proxyID uuid.NullUUID
	var nodeTexts []string
	err := row.Scan(&item.ID, &item.Alias, &item.Hostname, &item.Port, &item.Username,
		&item.CredentialID, &item.CredentialName, &proxyID, &item.ProxyJumpAlias,
		&item.Enabled, &item.UpdatedAt, pq.Array(&nodeTexts))
	if err != nil {
		return Host{}, err
	}
	if proxyID.Valid {
		item.ProxyJumpHostID = &proxyID.UUID
	}
	for _, value := range nodeTexts {
		id, err := uuid.Parse(value)
		if err != nil {
			return Host{}, err
		}
		item.ExecutionNodeIDs = append(item.ExecutionNodeIDs, id)
	}
	if item.ExecutionNodeIDs == nil {
		item.ExecutionNodeIDs = []uuid.UUID{}
	}
	return item, nil
}

func (s *Service) CreateHost(ctx context.Context, input HostInput) (Host, error) {
	input, err := normalizeHost(input)
	if err != nil {
		return Host{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Host{}, err
	}
	defer func() { _ = tx.Rollback() }()
	id := uuid.New()
	if _, err := tx.ExecContext(ctx, `INSERT INTO ssh_hosts
		(id, alias, hostname, port, username, credential_id, proxy_jump_host_id, enabled)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, id, input.Alias, input.Hostname, input.Port,
		input.Username, input.CredentialID, input.ProxyJumpHostID, enabledValue(input.Enabled)); err != nil {
		return Host{}, err
	}
	if err := replaceNodeAssignments(ctx, tx, id, input.ProxyJumpHostID,
		input.ExecutionNodeIDs, enabledValue(input.Enabled)); err != nil {
		return Host{}, err
	}
	if err := tx.Commit(); err != nil {
		return Host{}, err
	}
	return s.getHost(ctx, id)
}

func (s *Service) UpdateHost(ctx context.Context, id uuid.UUID, input HostInput) (Host, error) {
	input, err := normalizeHost(input)
	if err != nil {
		return Host{}, err
	}
	if input.ProxyJumpHostID != nil && *input.ProxyJumpHostID == id {
		return Host{}, errors.New("主机不能将自身设置为 ProxyJump")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Host{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE ssh_hosts SET alias=$2, hostname=$3, port=$4,
		username=$5, credential_id=$6, proxy_jump_host_id=$7, enabled=$8, updated_at=now()
		WHERE id=$1`, id, input.Alias, input.Hostname, input.Port, input.Username,
		input.CredentialID, input.ProxyJumpHostID, enabledValue(input.Enabled))
	if err != nil {
		return Host{}, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return Host{}, sql.ErrNoRows
	}
	if err := replaceNodeAssignments(ctx, tx, id, input.ProxyJumpHostID,
		input.ExecutionNodeIDs, enabledValue(input.Enabled)); err != nil {
		return Host{}, err
	}
	if err := tx.Commit(); err != nil {
		return Host{}, err
	}
	return s.getHost(ctx, id)
}

func (s *Service) DeleteHost(ctx context.Context, id uuid.UUID) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM ssh_hosts h WHERE h.id=$1
		AND NOT EXISTS (SELECT 1 FROM ssh_hosts child WHERE child.proxy_jump_host_id=h.id)`, id)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return errors.New("主机不存在或仍被其他主机用作 ProxyJump")
	}
	return nil
}

func replaceNodeAssignments(ctx context.Context, tx *sql.Tx, hostID uuid.UUID,
	proxyID *uuid.UUID, nodeIDs []uuid.UUID, enabled bool,
) error {
	if len(nodeIDs) > 0 {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM execution_nodes
			WHERE id = ANY($1::uuid[])`, pq.Array(nodeIDs)).Scan(&count); err != nil {
			return err
		}
		if count != len(nodeIDs) {
			return errors.New("包含不存在的 Execution Node")
		}
	}
	if proxyID != nil {
		var proxyEnabled bool
		if err := tx.QueryRowContext(ctx, `SELECT enabled FROM ssh_hosts WHERE id=$1`,
			*proxyID).Scan(&proxyEnabled); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM ssh_host_execution_nodes
			WHERE host_id=$1 AND execution_node_id = ANY($2::uuid[])`, *proxyID,
			pq.Array(nodeIDs)).Scan(&count); err != nil {
			return err
		}
		if enabled && (!proxyEnabled || count != len(nodeIDs)) {
			return errors.New("ProxyJump 主机必须分配到相同的 Execution Node")
		}
		var cycle bool
		if err := tx.QueryRowContext(ctx, `WITH RECURSIVE chain(id, proxy_id) AS (
			SELECT id, proxy_jump_host_id FROM ssh_hosts WHERE id=$1
			UNION SELECT h.id, h.proxy_jump_host_id FROM ssh_hosts h
			JOIN chain c ON h.id=c.proxy_id WHERE c.proxy_id IS NOT NULL)
			SELECT EXISTS(SELECT 1 FROM chain WHERE id=$2)`, *proxyID, hostID).Scan(&cycle); err != nil {
			return err
		}
		if cycle {
			return errors.New("ProxyJump 不能形成循环")
		}
	}
	var dependentAssignments int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM ssh_hosts child
		JOIN ssh_host_execution_nodes n ON n.host_id=child.id
		WHERE child.proxy_jump_host_id=$1 AND child.enabled
		AND (NOT $3 OR NOT n.execution_node_id = ANY(COALESCE($2::uuid[], '{}'::uuid[])))`, hostID,
		pq.Array(nodeIDs), enabled).Scan(&dependentAssignments); err != nil {
		return err
	}
	if dependentAssignments != 0 {
		return errors.New("该主机仍被已启用主机用作 ProxyJump，不能停用或移除其节点分配")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM ssh_host_execution_nodes WHERE host_id=$1`, hostID); err != nil {
		return err
	}
	for _, nodeID := range nodeIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO ssh_host_execution_nodes
			(host_id, execution_node_id) VALUES ($1,$2)`, hostID, nodeID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) getHost(ctx context.Context, id uuid.UUID) (Host, error) {
	return scanHost(s.db.QueryRowContext(ctx, `SELECT h.id, h.alias, h.hostname, h.port,
		h.username, h.credential_id, c.name, h.proxy_jump_host_id, COALESCE(p.alias,''),
		h.enabled, h.updated_at, COALESCE(array_agg(n.execution_node_id::text ORDER BY n.execution_node_id)
			FILTER (WHERE n.execution_node_id IS NOT NULL), '{}')
		FROM ssh_hosts h JOIN ssh_credentials c ON c.id=h.credential_id
		LEFT JOIN ssh_hosts p ON p.id=h.proxy_jump_host_id
		LEFT JOIN ssh_host_execution_nodes n ON n.host_id=h.id
		WHERE h.id=$1 GROUP BY h.id, c.name, p.alias`, id))
}
