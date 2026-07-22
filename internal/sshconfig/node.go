package sshconfig

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
)

type nodeRow struct {
	Host          NodeHost
	PublicKey     string
	Fingerprint   string
	CredentialVer int64
	SecretVer     int
}

func (s *Service) NodeConfiguration(ctx context.Context,
	nodeID uuid.UUID,
) (NodeConfiguration, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT h.alias, h.hostname, h.port, h.username,
		h.credential_id, COALESCE(p.alias,''), c.public_key, c.fingerprint, c.version,
		es.key_version
		FROM ssh_host_execution_nodes hn
		JOIN ssh_hosts h ON h.id=hn.host_id AND h.enabled
		JOIN ssh_credentials c ON c.id=h.credential_id AND c.enabled
		JOIN encrypted_secrets es ON es.id=c.secret_id
		LEFT JOIN ssh_hosts p ON p.id=h.proxy_jump_host_id
		WHERE hn.execution_node_id=$1 ORDER BY h.alias`, nodeID)
	if err != nil {
		return NodeConfiguration{}, err
	}
	defer func() { _ = rows.Close() }()
	var values []nodeRow
	for rows.Next() {
		var value nodeRow
		if err := rows.Scan(&value.Host.Alias, &value.Host.Hostname, &value.Host.Port,
			&value.Host.Username, &value.Host.CredentialID, &value.Host.ProxyJumpAlias,
			&value.PublicKey, &value.Fingerprint, &value.CredentialVer,
			&value.SecretVer); err != nil {
			return NodeConfiguration{}, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return NodeConfiguration{}, err
	}
	revisionData, _ := json.Marshal(values)
	digest := sha256.Sum256(revisionData)
	configuration := NodeConfiguration{Revision: hex.EncodeToString(digest[:]),
		Credentials: []NodeCredential{}, Hosts: []NodeHost{}}
	seen := make(map[uuid.UUID]bool)
	for _, value := range values {
		configuration.Hosts = append(configuration.Hosts, value.Host)
		if seen[value.Host.CredentialID] {
			continue
		}
		seen[value.Host.CredentialID] = true
		plain, err := s.secrets.Get(ctx, secretKey(value.Host.CredentialID))
		if err != nil {
			return NodeConfiguration{}, err
		}
		var payload secretPayload
		if err := json.Unmarshal(plain, &payload); err != nil {
			return NodeConfiguration{}, fmt.Errorf("解码 SSH 凭证 %s: %w",
				value.Host.CredentialID, err)
		}
		configuration.Credentials = append(configuration.Credentials, NodeCredential{
			ID: value.Host.CredentialID, PrivateKey: payload.PrivateKey,
			Passphrase: payload.Passphrase, PublicKey: value.PublicKey,
			Fingerprint: value.Fingerprint,
		})
	}
	return configuration, nil
}

func (s *Service) NodeCounts(ctx context.Context, nodeID uuid.UUID) (int, int, error) {
	var hosts, credentials int
	err := s.db.QueryRowContext(ctx, `SELECT count(*), count(DISTINCT h.credential_id)
		FROM ssh_host_execution_nodes n JOIN ssh_hosts h ON h.id=n.host_id AND h.enabled
		JOIN ssh_credentials c ON c.id=h.credential_id AND c.enabled
		WHERE n.execution_node_id=$1`, nodeID).Scan(&hosts, &credentials)
	if err == sql.ErrNoRows {
		return 0, 0, nil
	}
	return hosts, credentials, err
}
