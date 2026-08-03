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
	Host          WorkerHost
	PublicKey     string
	Fingerprint   string
	CredentialVer int64
	SecretVer     int
}

func (s *Service) WorkerConfiguration(ctx context.Context,
	workerID uuid.UUID,
) (WorkerConfiguration, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT h.alias, h.hostname, h.port, h.username,
		h.credential_id, COALESCE(p.alias,''), c.public_key, c.fingerprint, c.version,
		es.key_version
		FROM ssh_host_workers hn
		JOIN ssh_hosts h ON h.id=hn.host_id AND h.enabled
		JOIN ssh_credentials c ON c.id=h.credential_id AND c.enabled
		JOIN encrypted_secrets es ON es.id=c.secret_id
		LEFT JOIN ssh_hosts p ON p.id=h.proxy_jump_host_id
		WHERE hn.worker_id=$1 ORDER BY h.alias`, workerID)
	if err != nil {
		return WorkerConfiguration{}, err
	}
	defer func() { _ = rows.Close() }()
	var values []nodeRow
	for rows.Next() {
		var value nodeRow
		if err := rows.Scan(&value.Host.Alias, &value.Host.Hostname, &value.Host.Port,
			&value.Host.Username, &value.Host.CredentialID, &value.Host.ProxyJumpAlias,
			&value.PublicKey, &value.Fingerprint, &value.CredentialVer,
			&value.SecretVer); err != nil {
			return WorkerConfiguration{}, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return WorkerConfiguration{}, err
	}
	revisionData, _ := json.Marshal(values)
	digest := sha256.Sum256(revisionData)
	configuration := WorkerConfiguration{Revision: hex.EncodeToString(digest[:]),
		Credentials: []WorkerCredential{}, Hosts: []WorkerHost{}}
	seen := make(map[uuid.UUID]bool)
	for _, value := range values {
		configuration.Hosts = append(configuration.Hosts, value.Host)
		if seen[value.Host.CredentialID] {
			continue
		}
		seen[value.Host.CredentialID] = true
		plain, err := s.secrets.Get(ctx, secretKey(value.Host.CredentialID))
		if err != nil {
			return WorkerConfiguration{}, err
		}
		var payload secretPayload
		if err := json.Unmarshal(plain, &payload); err != nil {
			return WorkerConfiguration{}, fmt.Errorf("解码 SSH 凭证 %s: %w",
				value.Host.CredentialID, err)
		}
		configuration.Credentials = append(configuration.Credentials, WorkerCredential{
			ID: value.Host.CredentialID, PrivateKey: payload.PrivateKey,
			Passphrase: payload.Passphrase, PublicKey: value.PublicKey,
			Fingerprint: value.Fingerprint,
		})
	}
	return configuration, nil
}

func (s *Service) WorkerCounts(ctx context.Context, workerID uuid.UUID) (int, int, error) {
	var hosts, credentials int
	err := s.db.QueryRowContext(ctx, `SELECT count(*), count(DISTINCT h.credential_id)
		FROM ssh_host_workers n JOIN ssh_hosts h ON h.id=n.host_id AND h.enabled
		JOIN ssh_credentials c ON c.id=h.credential_id AND c.enabled
		WHERE n.worker_id=$1`, workerID).Scan(&hosts, &credentials)
	if err == sql.ErrNoRows {
		return 0, 0, nil
	}
	return hosts, credentials, err
}
