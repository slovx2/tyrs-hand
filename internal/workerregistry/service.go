package workerregistry

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/slovx2/tyrs-hand/internal/security"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

const (
	ProtocolVersion       = workerprotocol.Version
	GitHubDefaultSetting  = "worker.default.github"
	DiscordDefaultSetting = "worker.default.discord"
)

var (
	ErrUnauthorized               = errors.New("Worker凭据无效")
	ErrDisabled                   = errors.New("Worker已经禁用")
	ErrIncompatible               = errors.New("Worker协议版本不兼容")
	ErrInvalidHostKeyFingerprint  = errors.New("Worker SSH Host Key 指纹无效")
	ErrHostKeyFingerprintChanged  = errors.New("Worker SSH Host Key 指纹发生变化")
	ErrHostKeyFingerprintConflict = errors.New("Worker SSH Host Key 指纹已属于另一台 Worker")
)

type Worker struct {
	ID                    uuid.UUID       `json:"id"`
	Name                  string          `json:"name"`
	Roles                 []string        `json:"roles"`
	Enabled               bool            `json:"enabled"`
	MaxConcurrentJobs     int             `json:"maxConcurrentJobs"`
	ProtocolVersion       int             `json:"protocolVersion"`
	WorkerVersion         string          `json:"workerVersion,omitempty"`
	Status                string          `json:"status"`
	HeartbeatAt           *time.Time      `json:"heartbeatAt,omitempty"`
	LastError             string          `json:"lastError,omitempty"`
	SSHHostKeyFingerprint string          `json:"sshHostKeyFingerprint,omitempty"`
	Metadata              json.RawMessage `json:"metadata"`
}

type Defaults struct {
	GitHubWorkerID  *uuid.UUID `json:"githubWorkerId"`
	DiscordWorkerID *uuid.UUID `json:"discordWorkerId"`
}

type Service struct {
	db *sql.DB
}

func NewService(db *sql.DB) *Service { return &Service{db: db} }

func normalizeRoles(roles []string) ([]string, error) {
	result := make([]string, 0, len(roles))
	for _, role := range roles {
		role = strings.TrimSpace(role)
		if role != "discord" {
			return nil, fmt.Errorf("不支持的Worker角色 %q", role)
		}
		if !slices.Contains(result, role) {
			result = append(result, role)
		}
	}
	if len(result) == 0 {
		return nil, errors.New("Worker至少需要一个角色")
	}
	return result, nil
}

func (s *Service) Create(ctx context.Context, name string, roles []string, maxJobs int) (Worker, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 128 {
		return Worker{}, "", errors.New("worker 名称必须为 1 到 128 个字符")
	}
	roles, err := normalizeRoles(roles)
	if err != nil {
		return Worker{}, "", err
	}
	if maxJobs <= 0 {
		maxJobs = 6
	}
	encoded, _ := json.Marshal(roles)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Worker{}, "", err
	}
	defer func() { _ = tx.Rollback() }()
	var worker Worker
	worker.Roles, worker.Metadata = roles, json.RawMessage(`{}`)
	err = tx.QueryRowContext(ctx, `INSERT INTO workers
		(name, roles, max_concurrent_jobs, protocol_version)
		VALUES ($1,$2,$3,$4)
		RETURNING id, name, enabled, max_concurrent_jobs, protocol_version, status`,
		name, encoded, maxJobs, ProtocolVersion).Scan(&worker.ID, &worker.Name, &worker.Enabled,
		&worker.MaxConcurrentJobs, &worker.ProtocolVersion, &worker.Status)
	if err != nil {
		return Worker{}, "", err
	}
	token, err := security.RandomToken(32)
	if err != nil {
		return Worker{}, "", err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO worker_enrollments
		(worker_id, token_hash, expires_at) VALUES ($1,$2,now() + interval '15 minutes')`,
		worker.ID, security.Digest(token))
	if err != nil {
		return Worker{}, "", err
	}
	return worker, token, tx.Commit()
}

func (s *Service) NewEnrollment(ctx context.Context, workerID uuid.UUID) (string, error) {
	var enabled bool
	if err := s.db.QueryRowContext(ctx, `SELECT enabled FROM workers WHERE id = $1`, workerID).Scan(&enabled); err != nil {
		return "", err
	}
	if !enabled {
		return "", ErrDisabled
	}
	token, err := security.RandomToken(32)
	if err != nil {
		return "", err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO worker_enrollments
		(worker_id, token_hash, expires_at) VALUES ($1,$2,now() + interval '15 minutes')`,
		workerID, security.Digest(token))
	return token, err
}

func (s *Service) Enroll(ctx context.Context, token string) (Worker, string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Worker{}, "", err
	}
	defer func() { _ = tx.Rollback() }()
	var workerID uuid.UUID
	err = tx.QueryRowContext(ctx, `UPDATE worker_enrollments SET consumed_at = now()
		WHERE token_hash = $1 AND consumed_at IS NULL AND expires_at > now()
		RETURNING worker_id`, security.Digest(token)).Scan(&workerID)
	if errors.Is(err, sql.ErrNoRows) {
		return Worker{}, "", ErrUnauthorized
	}
	if err != nil {
		return Worker{}, "", err
	}
	credential, err := security.RandomToken(32)
	if err != nil {
		return Worker{}, "", err
	}
	updated, err := tx.ExecContext(ctx, `UPDATE workers SET credential_hash = $2,
		credential_version = credential_version + 1, status = 'offline', last_error = NULL,
		updated_at = now() WHERE id = $1 AND enabled`, workerID, security.Digest(credential))
	if err != nil {
		return Worker{}, "", err
	}
	if count, _ := updated.RowsAffected(); count != 1 {
		return Worker{}, "", ErrDisabled
	}
	worker, err := scanWorker(tx.QueryRowContext(ctx, workerSelect+" WHERE id = $1", workerID))
	if err != nil {
		return Worker{}, "", err
	}
	return worker, credential, tx.Commit()
}

const workerSelect = `SELECT id, name, roles, enabled, max_concurrent_jobs, protocol_version,
	COALESCE(worker_version,''), status, heartbeat_at, COALESCE(last_error,''),
	COALESCE(ssh_host_key_fingerprint,''), metadata
	FROM workers`

type scanner interface{ Scan(...any) error }

func scanWorker(row scanner) (Worker, error) {
	var worker Worker
	var roles []byte
	var heartbeat sql.NullTime
	err := row.Scan(&worker.ID, &worker.Name, &roles, &worker.Enabled, &worker.MaxConcurrentJobs,
		&worker.ProtocolVersion, &worker.WorkerVersion, &worker.Status, &heartbeat, &worker.LastError,
		&worker.SSHHostKeyFingerprint, &worker.Metadata)
	if err != nil {
		return Worker{}, err
	}
	if err := json.Unmarshal(roles, &worker.Roles); err != nil {
		return Worker{}, err
	}
	if heartbeat.Valid {
		worker.HeartbeatAt = &heartbeat.Time
	}
	return worker, nil
}

func (s *Service) Authenticate(ctx context.Context, credential string) (Worker, error) {
	if credential == "" {
		return Worker{}, ErrUnauthorized
	}
	worker, err := scanWorker(s.db.QueryRowContext(ctx, workerSelect+" WHERE credential_hash = $1",
		security.Digest(credential)))
	if errors.Is(err, sql.ErrNoRows) {
		return Worker{}, ErrUnauthorized
	}
	if err != nil {
		return Worker{}, err
	}
	if !worker.Enabled {
		return Worker{}, ErrDisabled
	}
	if worker.ProtocolVersion != ProtocolVersion {
		return Worker{}, ErrIncompatible
	}
	return worker, nil
}

func (s *Service) Heartbeat(ctx context.Context, id uuid.UUID, version string,
	protocolVersion int, sshHostKeyFingerprint string, metadata json.RawMessage,
) error {
	sshHostKeyFingerprint = strings.TrimSpace(sshHostKeyFingerprint)
	if !validSSHHostKeyFingerprint(sshHostKeyFingerprint) {
		return ErrInvalidHostKeyFingerprint
	}
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}
	status, lastError := "online", ""
	if protocolVersion != ProtocolVersion {
		status = "incompatible"
		lastError = fmt.Sprintf("Worker 协议版本 %d，Control 要求 %d", protocolVersion,
			ProtocolVersion)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE workers SET worker_version = $2,
		metadata = $3, status = $4, heartbeat_at = now(), last_error = NULLIF($5,''),
		ssh_host_key_fingerprint = COALESCE(ssh_host_key_fingerprint,$6), updated_at = now()
		WHERE id = $1 AND enabled
			AND (ssh_host_key_fingerprint IS NULL OR ssh_host_key_fingerprint=$6)`,
		id, strings.TrimSpace(version), metadata, status, lastError, sshHostKeyFingerprint)
	if err != nil {
		var databaseError *pq.Error
		if errors.As(err, &databaseError) && databaseError.Code == "23505" &&
			databaseError.Constraint == "workers_ssh_host_key_fingerprint_unique" {
			return ErrHostKeyFingerprintConflict
		}
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated == 1 {
		return nil
	}
	var current sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT ssh_host_key_fingerprint FROM workers
		WHERE id=$1`, id).Scan(&current); err != nil {
		return err
	}
	if current.Valid && current.String != sshHostKeyFingerprint {
		return ErrHostKeyFingerprintChanged
	}
	return ErrDisabled
}

func validSSHHostKeyFingerprint(value string) bool {
	const prefix = "SHA256:"
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+43 {
		return false
	}
	decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(value, prefix))
	return err == nil && len(decoded) == 32
}

func (s *Service) List(ctx context.Context) ([]Worker, error) {
	rows, err := s.db.QueryContext(ctx, workerSelect+" ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make([]Worker, 0)
	for rows.Next() {
		worker, err := scanWorker(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, worker)
		if worker.Status == "online" && worker.HeartbeatAt != nil &&
			time.Since(*worker.HeartbeatAt) > 2*time.Minute {
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
	result, err := s.db.ExecContext(ctx, `UPDATE workers SET enabled = $2,
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
	result, err := s.db.ExecContext(ctx, `DELETE FROM workers n WHERE n.id = $1
		AND NOT EXISTS (SELECT 1 FROM work_items WHERE worker_id = n.id)
		AND NOT EXISTS (SELECT 1 FROM worker_workspaces WHERE worker_id = n.id)
		AND NOT EXISTS (SELECT 1 FROM codex_thread_controls WHERE worker_id = n.id)
		AND NOT EXISTS (SELECT 1 FROM platform_settings WHERE setting_key IN ($2,$3)
			AND value->>'workerId' = n.id::text)`, id, GitHubDefaultSetting, DiscordDefaultSetting)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("Worker不存在或仍被资源引用")
	}
	return nil
}

func (s *Service) Defaults(ctx context.Context) (Defaults, error) {
	var result Defaults
	rows, err := s.db.QueryContext(ctx, `SELECT setting_key, value->>'workerId'
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
			result.GitHubWorkerID = &id
		} else {
			result.DiscordWorkerID = &id
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
	}{{GitHubDefaultSetting, "github", defaults.GitHubWorkerID},
		{DiscordDefaultSetting, "discord", defaults.DiscordWorkerID}} {
		if item.id == nil {
			if _, err := tx.ExecContext(ctx, `DELETE FROM platform_settings WHERE setting_key = $1`, item.key); err != nil {
				return err
			}
			continue
		}
		var valid bool
		if err := tx.QueryRowContext(ctx, `SELECT enabled AND roles ? $2 FROM workers WHERE id = $1`,
			*item.id, item.role).Scan(&valid); err != nil {
			return err
		}
		if !valid {
			return fmt.Errorf("节点 %s 未启用或不支持 %s 角色", item.id.String(), item.role)
		}
		value, _ := json.Marshal(map[string]string{"workerId": item.id.String()})
		if _, err := tx.ExecContext(ctx, `INSERT INTO platform_settings(setting_key, value)
			VALUES ($1,$2) ON CONFLICT(setting_key) DO UPDATE SET value = EXCLUDED.value,
			version = platform_settings.version + 1, updated_at = now()`, item.key, value); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE work_items w
		SET worker_id = (setting.value->>'workerId')::uuid, updated_at = now()
		FROM platform_settings setting
		WHERE setting.setting_key = $1 AND w.worker_id IS NULL AND EXISTS (
			SELECT 1 FROM codex_thread_controls c JOIN codex_turn_intents i ON i.control_id = c.id
			WHERE c.work_item_id = w.id AND i.status = 'placement_pending')`, GitHubDefaultSetting)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE codex_thread_controls c
		SET worker_id = w.worker_id, updated_at = now()
		FROM work_items w WHERE c.work_item_id = w.id AND c.worker_id IS NULL
		AND w.worker_id IS NOT NULL`)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE codex_turn_intents i SET status = 'queued', updated_at = now()
		FROM codex_thread_controls c WHERE i.control_id = c.id AND i.status = 'placement_pending'
		AND c.worker_id IS NOT NULL`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func HasRole(worker Worker, role string) bool {
	if role == "all" {
		return slices.Contains(worker.Roles, "github") && slices.Contains(worker.Roles, "discord")
	}
	return slices.Contains(worker.Roles, role)
}
