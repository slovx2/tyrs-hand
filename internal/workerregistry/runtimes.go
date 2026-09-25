package workerregistry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

var ErrInvalidRuntimeReport = errors.New("运行时心跳快照无效")

type Runtime struct {
	workerprotocol.RuntimeReport
	WorkerID    uuid.UUID  `json:"workerId"`
	Enabled     bool       `json:"enabled"`
	HeartbeatAt *time.Time `json:"heartbeatAt"`
}

func validateRuntimeReports(reports []workerprotocol.RuntimeReport, codexFingerprint string) error {
	if len(reports) < 1 || len(reports) > 2 {
		return fmt.Errorf("%w: 必须上报一个或两个引擎", ErrInvalidRuntimeReport)
	}
	seen := map[runtimeidentity.Engine]bool{}
	keys := map[string]bool{}
	ports := map[int]bool{}
	for _, report := range reports {
		if err := report.Engine.Validate(); err != nil || seen[report.Engine] {
			return fmt.Errorf("%w: 引擎未知或重复", ErrInvalidRuntimeReport)
		}
		seen[report.Engine] = true
		if report.Status != "running" && report.Status != "unavailable" && report.Status != "stopped" {
			return fmt.Errorf("%w: 引擎状态无效", ErrInvalidRuntimeReport)
		}
		if !validSSHHostKeyFingerprint(report.SSHHostKeyFingerprint) {
			return ErrInvalidHostKeyFingerprint
		}
		if keys[report.SSHHostKeyFingerprint] {
			return fmt.Errorf("%w: 两引擎必须使用独立 Host Key", ErrInvalidRuntimeReport)
		}
		keys[report.SSHHostKeyFingerprint] = true
		if report.Engine == runtimeidentity.Codex && report.SSHHostKeyFingerprint != codexFingerprint {
			return fmt.Errorf("%w: Codex 指纹与 Worker 入口不一致", ErrInvalidRuntimeReport)
		}
		_, portText, err := net.SplitHostPort(report.SSHListenAddress)
		port, parseErr := strconv.Atoi(portText)
		if err != nil || parseErr != nil || port < 1 || port > 65535 || ports[port] {
			return fmt.Errorf("%w: SSH 端口无效或冲突", ErrInvalidRuntimeReport)
		}
		ports[port] = true
		if report.ProtocolVersion == "" || (report.Status == "running" && report.Build.CLIBuild == "") || report.Capabilities == nil {
			return fmt.Errorf("%w: 缺少协议、构建或能力声明", ErrInvalidRuntimeReport)
		}
		if len(report.ModelCatalog) > 0 {
			var catalog map[string]json.RawMessage
			if json.Unmarshal(report.ModelCatalog, &catalog) != nil {
				return fmt.Errorf("%w: 模型目录必须是对象或 null", ErrInvalidRuntimeReport)
			}
		}
	}
	if !seen[runtimeidentity.Codex] {
		return fmt.Errorf("%w: 缺少 Codex 入口", ErrInvalidRuntimeReport)
	}
	return nil
}

func saveRuntimeReports(ctx context.Context, tx *sql.Tx, id uuid.UUID, reports []workerprotocol.RuntimeReport, incompatible bool) error {
	// 上层已锁定 Worker 行，完整快照更新与禁用操作在同一事务提交。
	if _, err := tx.ExecContext(ctx, `UPDATE worker_runtimes SET enabled=false,status='disabled'
		WHERE worker_id=$1`, id); err != nil {
		return err
	}
	for _, report := range reports {
		build, err := json.Marshal(report.Build)
		if err != nil {
			return err
		}
		capabilities, err := json.Marshal(report.Capabilities)
		if err != nil {
			return err
		}
		status := report.Status
		if incompatible {
			status = "incompatible"
		}
		var catalog any
		if len(report.ModelCatalog) > 0 {
			catalog = []byte(report.ModelCatalog)
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO worker_runtimes
			(worker_id,engine,enabled,status,ssh_listen_address,ssh_host_key_fingerprint,
			protocol_version,build,capabilities,model_catalog,release_ready,heartbeat_at)
			VALUES ($1,$2,true,$3,$4,$5,$6,$7,$8,$9,$10,now())
			ON CONFLICT(worker_id,engine) DO UPDATE SET enabled=true,status=EXCLUDED.status,
			ssh_listen_address=EXCLUDED.ssh_listen_address,
			ssh_host_key_fingerprint=EXCLUDED.ssh_host_key_fingerprint,
			protocol_version=EXCLUDED.protocol_version,build=EXCLUDED.build,
			capabilities=EXCLUDED.capabilities,model_catalog=EXCLUDED.model_catalog,
			release_ready=EXCLUDED.release_ready,heartbeat_at=EXCLUDED.heartbeat_at
			WHERE worker_runtimes.ssh_host_key_fingerprint IS NULL
			OR worker_runtimes.ssh_host_key_fingerprint=EXCLUDED.ssh_host_key_fingerprint`,
			id, report.Engine, status, report.SSHListenAddress, report.SSHHostKeyFingerprint,
			report.ProtocolVersion, build, capabilities, catalog, report.ReleaseReady)
		if err != nil {
			var constraint *pq.Error
			if errors.As(err, &constraint) && constraint.Code == "23505" && constraint.Constraint == "worker_runtimes_host_key_unique" {
				return ErrHostKeyFingerprintConflict
			}
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return ErrHostKeyFingerprintChanged
		}
	}
	return nil
}

func (s *Service) Runtimes(ctx context.Context, workerID uuid.UUID) ([]Runtime, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.engine,r.enabled,
		CASE WHEN NOT w.enabled OR NOT r.enabled THEN 'disabled'
			WHEN r.heartbeat_at IS NULL OR r.heartbeat_at < now()-interval '2 minutes' THEN 'offline'
			ELSE r.status END,
		r.ssh_listen_address,COALESCE(r.ssh_host_key_fingerprint,''),r.protocol_version,
		r.build,r.capabilities,r.model_catalog,r.release_ready,r.heartbeat_at
		FROM worker_runtimes r JOIN workers w ON w.id=r.worker_id
		WHERE r.worker_id=$1 ORDER BY r.engine DESC`, workerID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make([]Runtime, 0, 2)
	for rows.Next() {
		runtime := Runtime{WorkerID: workerID}
		var build, capabilities []byte
		if err := rows.Scan(&runtime.Engine, &runtime.Enabled, &runtime.Status,
			&runtime.SSHListenAddress, &runtime.SSHHostKeyFingerprint, &runtime.ProtocolVersion,
			&build, &capabilities, &runtime.ModelCatalog, &runtime.ReleaseReady, &runtime.HeartbeatAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(build, &runtime.Build); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(capabilities, &runtime.Capabilities); err != nil {
			return nil, err
		}
		result = append(result, runtime)
	}
	return result, rows.Err()
}
