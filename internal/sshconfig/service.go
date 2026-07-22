package sshconfig

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/secrets"
)

type Service struct {
	db      *sql.DB
	secrets *secrets.Store
}

func NewService(db *sql.DB, secretStore *secrets.Store) *Service {
	return &Service{db: db, secrets: secretStore}
}

func secretKey(id uuid.UUID) string { return "ssh.credential." + id.String() }

func normalizeCredential(input CredentialInput, requireKey bool) (CredentialInput, string, string, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 128 {
		return input, "", "", errors.New("凭证名称必须为 1 到 128 个字符")
	}
	if !requireKey && strings.TrimSpace(input.PrivateKey) == "" {
		return input, "", "", nil
	}
	publicKey, fingerprint, err := parsePrivateKey(input.PrivateKey, input.Passphrase)
	return input, publicKey, fingerprint, err
}

func (s *Service) ListCredentials(ctx context.Context) ([]Credential, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.id, c.name, c.public_key, c.fingerprint,
		c.enabled, c.version, count(h.id), c.updated_at
		FROM ssh_credentials c LEFT JOIN ssh_hosts h ON h.credential_id = c.id
		GROUP BY c.id ORDER BY c.name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []Credential
	for rows.Next() {
		var item Credential
		if err := rows.Scan(&item.ID, &item.Name, &item.PublicKey, &item.Fingerprint,
			&item.Enabled, &item.Version, &item.HostCount, &item.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Service) CreateCredential(ctx context.Context, input CredentialInput) (Credential, error) {
	input, publicKey, fingerprint, err := normalizeCredential(input, true)
	if err != nil {
		return Credential{}, err
	}
	id := uuid.New()
	payload, _ := json.Marshal(secretPayload{PrivateKey: strings.TrimSpace(input.PrivateKey),
		Passphrase: input.Passphrase})
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Credential{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.secrets.PutTx(ctx, tx, secretKey(id), payload); err != nil {
		return Credential{}, err
	}
	var secretID uuid.UUID
	if err := tx.QueryRowContext(ctx, `SELECT id FROM encrypted_secrets WHERE secret_key = $1`,
		secretKey(id)).Scan(&secretID); err != nil {
		return Credential{}, err
	}
	var result Credential
	err = tx.QueryRowContext(ctx, `INSERT INTO ssh_credentials
		(id, name, secret_id, public_key, fingerprint, enabled)
		VALUES ($1,$2,$3,$4,$5,$6)
		RETURNING id, name, public_key, fingerprint, enabled, version, updated_at`,
		id, input.Name, secretID, publicKey, fingerprint, enabledValue(input.Enabled)).Scan(
		&result.ID, &result.Name, &result.PublicKey, &result.Fingerprint, &result.Enabled,
		&result.Version, &result.UpdatedAt)
	if err != nil {
		return Credential{}, err
	}
	return result, tx.Commit()
}

func (s *Service) UpdateCredential(ctx context.Context, id uuid.UUID,
	input CredentialInput,
) (Credential, error) {
	input, publicKey, fingerprint, err := normalizeCredential(input, false)
	if err != nil {
		return Credential{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Credential{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if publicKey != "" {
		payload, _ := json.Marshal(secretPayload{PrivateKey: strings.TrimSpace(input.PrivateKey),
			Passphrase: input.Passphrase})
		if err := s.secrets.PutTx(ctx, tx, secretKey(id), payload); err != nil {
			return Credential{}, err
		}
	}
	var result Credential
	query := `UPDATE ssh_credentials SET name = $2, enabled = $3, version = version + 1,
		updated_at = now()`
	arguments := []any{id, input.Name, enabledValue(input.Enabled)}
	if publicKey != "" {
		query += `, public_key = $4, fingerprint = $5`
		arguments = append(arguments, publicKey, fingerprint)
	}
	query += ` WHERE id = $1 RETURNING id, name, public_key, fingerprint, enabled, version, updated_at`
	err = tx.QueryRowContext(ctx, query, arguments...).Scan(&result.ID, &result.Name,
		&result.PublicKey, &result.Fingerprint, &result.Enabled, &result.Version, &result.UpdatedAt)
	if err != nil {
		return Credential{}, err
	}
	return result, tx.Commit()
}

func (s *Service) DeleteCredential(ctx context.Context, id uuid.UUID) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var secretID uuid.UUID
	err = tx.QueryRowContext(ctx, `DELETE FROM ssh_credentials c WHERE c.id = $1
		AND NOT EXISTS (SELECT 1 FROM ssh_hosts h WHERE h.credential_id = c.id)
		RETURNING secret_id`, id).Scan(&secretID)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("凭证不存在或仍有关联主机")
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM encrypted_secrets WHERE id = $1`, secretID); err != nil {
		return fmt.Errorf("删除加密凭证: %w", err)
	}
	return tx.Commit()
}
