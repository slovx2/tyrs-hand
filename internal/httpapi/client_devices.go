package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"image/png"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/boombuler/barcode"
	"github.com/boombuler/barcode/qr"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/auth"
	"github.com/slovx2/tyrs-hand/internal/security"
)

const clientDevicePairingLifetime = 10 * time.Minute

type clientDevice struct {
	ID         uuid.UUID       `json:"id"`
	Name       string          `json:"name"`
	Platform   string          `json:"platform"`
	CreatedAt  time.Time       `json:"createdAt"`
	ApprovedAt time.Time       `json:"approvedAt"`
	LastSeenAt *time.Time      `json:"lastSeenAt,omitempty"`
	Machines   []clientMachine `json:"machines"`
}

type clientDevicePairing struct {
	ID                    uuid.UUID  `json:"id"`
	Status                string     `json:"status"`
	DeviceID              *uuid.UUID `json:"deviceId,omitempty"`
	DeviceName            *string    `json:"deviceName,omitempty"`
	Platform              *string    `json:"platform,omitempty"`
	WorkerID              uuid.UUID  `json:"workerId"`
	WorkerName            string     `json:"workerName"`
	SSHHostKeyFingerprint string     `json:"sshHostKeyFingerprint"`
	ExpiresAt             time.Time  `json:"expiresAt"`
	CreatedAt             time.Time  `json:"createdAt"`
}

type createClientDevicePairingRequest struct {
	WorkerID uuid.UUID `json:"workerId" binding:"required"`
}

type claimClientDeviceRequest struct {
	PairingSecret  string    `json:"pairingSecret" binding:"required"`
	DeviceID       uuid.UUID `json:"deviceId" binding:"required"`
	Name           string    `json:"name" binding:"required"`
	Platform       string    `json:"platform" binding:"required"`
	CredentialHash string    `json:"credentialHash" binding:"required"`
}

func (s *Server) listClientDevices(c *gin.Context) {
	administratorID := c.MustGet("session").(auth.Session).AdministratorID
	rows, err := s.db.QueryContext(c.Request.Context(), `
		SELECT id,name,platform,created_at,approved_at,last_seen_at
		FROM client_devices WHERE administrator_id=$1 ORDER BY created_at DESC`, administratorID)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取设备列表失败", err)
		return
	}
	defer func() { _ = rows.Close() }()
	items := make([]clientDevice, 0)
	byID := make(map[uuid.UUID]int)
	for rows.Next() {
		var item clientDevice
		if err := rows.Scan(&item.ID, &item.Name, &item.Platform, &item.CreatedAt,
			&item.ApprovedAt, &item.LastSeenAt); err != nil {
			problem(c, http.StatusInternalServerError, "读取设备列表失败", err)
			return
		}
		item.Machines = make([]clientMachine, 0)
		byID[item.ID] = len(items)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		problem(c, http.StatusInternalServerError, "读取设备列表失败", err)
		return
	}
	machineRows, err := s.db.QueryContext(c.Request.Context(), `
		SELECT binding.device_id,worker.id,worker.name,binding.ssh_host_key_fingerprint,
			worker.status,worker.heartbeat_at,workspace.id,binding.approved_at
		FROM client_device_workers binding
		JOIN client_devices device ON device.id=binding.device_id
		JOIN workers worker ON worker.id=binding.worker_id
		LEFT JOIN worker_workspaces workspace ON workspace.worker_id=worker.id
		WHERE device.administrator_id=$1
		ORDER BY worker.name`, administratorID)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取设备机器列表失败", err)
		return
	}
	defer func() { _ = machineRows.Close() }()
	for machineRows.Next() {
		var deviceID uuid.UUID
		var item clientMachine
		var heartbeat sql.NullTime
		var workspaceID uuid.NullUUID
		if err := machineRows.Scan(&deviceID, &item.WorkerID, &item.Name,
			&item.SSHHostKeyFingerprint, &item.Status, &heartbeat, &workspaceID,
			&item.ApprovedAt); err != nil {
			problem(c, http.StatusInternalServerError, "读取设备机器列表失败", err)
			return
		}
		item.HeartbeatAt = clientNullableTime(heartbeat)
		item.WorkspaceID = clientNullableUUID(workspaceID)
		if index, ok := byID[deviceID]; ok {
			items[index].Machines = append(items[index].Machines, item)
		}
	}
	if err := machineRows.Err(); err != nil {
		problem(c, http.StatusInternalServerError, "读取设备机器列表失败", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (s *Server) createClientDevicePairing(c *gin.Context) {
	administratorID := c.MustGet("session").(auth.Session).AdministratorID
	var request createClientDevicePairingRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.WorkerID == uuid.Nil {
		badRequest(c, errors.New("必须选择 Worker"))
		return
	}
	var workerName, fingerprint string
	err := s.db.QueryRowContext(c.Request.Context(), `SELECT name,
		COALESCE(ssh_host_key_fingerprint,'') FROM workers WHERE id=$1 AND enabled`,
		request.WorkerID).Scan(&workerName, &fingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusNotFound, "Worker 不存在", err)
		return
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Worker 失败", err)
		return
	}
	if fingerprint == "" {
		problem(c, http.StatusConflict, "Worker 尚未上报 SSH Host Key，不能生成二维码", nil)
		return
	}
	secret, err := security.RandomToken(32)
	if err != nil {
		problem(c, http.StatusInternalServerError, "创建设备绑定失败", err)
		return
	}
	pairing := clientDevicePairing{ID: uuid.New(), Status: "waiting_scan",
		WorkerID: request.WorkerID, WorkerName: workerName, SSHHostKeyFingerprint: fingerprint,
		ExpiresAt: time.Now().UTC().Add(clientDevicePairingLifetime), CreatedAt: time.Now().UTC()}
	_, err = s.db.ExecContext(c.Request.Context(), `
		INSERT INTO client_device_pairings(id,administrator_id,pairing_secret_hash,
			worker_id,ssh_host_key_fingerprint,expires_at,created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`, pairing.ID, administratorID,
		security.Digest(secret), pairing.WorkerID, pairing.SSHHostKeyFingerprint,
		pairing.ExpiresAt, pairing.CreatedAt)
	if err != nil {
		problem(c, http.StatusInternalServerError, "创建设备绑定失败", err)
		return
	}
	serverID, err := s.clientControlInstanceID(c.Request.Context())
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Control 身份失败", err)
		return
	}
	pairingURI := devicePairingURI(s.cfg.PublicURL, serverID, pairing.ID, secret,
		pairing.WorkerID, pairing.WorkerName, pairing.SSHHostKeyFingerprint, pairing.ExpiresAt)
	qrDataURL, err := qrDataURL(pairingURI)
	if err != nil {
		problem(c, http.StatusInternalServerError, "生成设备二维码失败", err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"id": pairing.ID, "status": pairing.Status, "expiresAt": pairing.ExpiresAt,
		"createdAt": pairing.CreatedAt, "serverId": serverID,
		"workerId": pairing.WorkerID, "workerName": pairing.WorkerName,
		"sshHostKeyFingerprint": pairing.SSHHostKeyFingerprint,
		"pairingUri":            pairingURI, "qrDataUrl": qrDataURL,
	})
}

func (s *Server) getClientDevicePairing(c *gin.Context) {
	pairingID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	pairing, err := s.loadAdministratorPairing(c, pairingID)
	if errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusNotFound, "设备绑定不存在", err)
		return
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取设备绑定失败", err)
		return
	}
	c.JSON(http.StatusOK, pairing)
}

func (s *Server) claimClientDevicePairing(c *gin.Context) {
	pairingID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	var request claimClientDeviceRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	request.Name = strings.TrimSpace(request.Name)
	request.Platform = strings.TrimSpace(request.Platform)
	if request.DeviceID == uuid.Nil || request.Name == "" || request.Platform == "" ||
		len(request.Name) > 120 || len(request.Platform) > 40 || len(request.CredentialHash) != 64 {
		badRequest(c, errors.New("设备绑定参数无效"))
		return
	}
	claimToken, err := security.RandomToken(32)
	if err != nil {
		problem(c, http.StatusInternalServerError, "提交设备绑定失败", err)
		return
	}
	result, err := s.db.ExecContext(c.Request.Context(), `
		UPDATE client_device_pairings SET status='waiting_confirmation',claim_token_hash=$1,
			device_id=$2,device_name=$3,platform=$4,credential_hash=$5,claimed_at=now(),updated_at=now()
		WHERE id=$6 AND pairing_secret_hash=$7 AND status='waiting_scan' AND expires_at>now()
			AND worker_id IS NOT NULL
			AND (NOT EXISTS (SELECT 1 FROM client_devices WHERE id=$2 OR credential_hash=$5)
				OR EXISTS (SELECT 1 FROM client_devices device
					WHERE device.id=$2 AND device.credential_hash=$5
						AND device.administrator_id=client_device_pairings.administrator_id))`,
		security.Digest(claimToken), request.DeviceID, request.Name, request.Platform,
		request.CredentialHash, pairingID, security.Digest(request.PairingSecret))
	if err != nil {
		problem(c, http.StatusInternalServerError, "提交设备绑定失败", err)
		return
	}
	updated, err := result.RowsAffected()
	if err != nil {
		problem(c, http.StatusInternalServerError, "提交设备绑定失败", err)
		return
	}
	if updated != 1 {
		problem(c, http.StatusUnauthorized, "设备绑定二维码无效或已过期", nil)
		return
	}
	c.JSON(http.StatusOK, gin.H{"claimToken": claimToken, "status": "waiting_confirmation"})
}

func (s *Server) clientDevicePairingStatus(c *gin.Context) {
	pairingID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	claimToken := strings.TrimSpace(strings.TrimPrefix(c.GetHeader("Authorization"), "Pairing "))
	if claimToken == "" || !strings.HasPrefix(c.GetHeader("Authorization"), "Pairing ") {
		problem(c, http.StatusUnauthorized, "需要设备绑定凭证", nil)
		return
	}
	var status string
	var expiresAt time.Time
	err = s.db.QueryRowContext(c.Request.Context(), `
		SELECT status,expires_at FROM client_device_pairings
		WHERE id=$1 AND claim_token_hash=$2`, pairingID, security.Digest(claimToken)).
		Scan(&status, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusUnauthorized, "设备绑定凭证无效", err)
		return
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取设备绑定状态失败", err)
		return
	}
	if time.Now().UTC().After(expiresAt) && status != "approved" {
		status = "expired"
	}
	c.JSON(http.StatusOK, gin.H{"status": status, "expiresAt": expiresAt})
}

func (s *Server) approveClientDevicePairing(c *gin.Context) {
	administratorID := c.MustGet("session").(auth.Session).AdministratorID
	pairingID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "确认设备绑定失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var device clientDevice
	var workerID uuid.UUID
	var fingerprint string
	err = tx.QueryRowContext(c.Request.Context(), `
		UPDATE client_device_pairings SET status='approved',confirmed_at=now(),updated_at=now()
		WHERE id=$1 AND administrator_id=$2 AND status='waiting_confirmation' AND expires_at>now()
		RETURNING device_id,device_name,platform,worker_id,ssh_host_key_fingerprint,now(),now()`,
		pairingID, administratorID).Scan(&device.ID, &device.Name, &device.Platform, &workerID,
		&fingerprint, &device.CreatedAt, &device.ApprovedAt)
	if errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusConflict, "设备绑定已失效或当前不可确认", err)
		return
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "确认设备绑定失败", err)
		return
	}
	_, err = tx.ExecContext(c.Request.Context(), `
		INSERT INTO client_devices(id,administrator_id,name,platform,credential_hash,created_at,approved_at)
		SELECT device_id,administrator_id,device_name,platform,credential_hash,$3,$3
		FROM client_device_pairings WHERE id=$1 AND administrator_id=$2
		ON CONFLICT(id) DO UPDATE SET name=EXCLUDED.name,platform=EXCLUDED.platform,
			last_seen_at=now()
		WHERE client_devices.administrator_id=EXCLUDED.administrator_id
			AND client_devices.credential_hash=EXCLUDED.credential_hash`,
		pairingID, administratorID, device.ApprovedAt)
	if err != nil {
		problem(c, http.StatusConflict, "设备已存在，无法重复确认", err)
		return
	}
	_, err = tx.ExecContext(c.Request.Context(), `INSERT INTO client_device_workers(
		device_id,worker_id,ssh_host_key_fingerprint,approved_at)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT(device_id,worker_id) DO UPDATE SET
			ssh_host_key_fingerprint=EXCLUDED.ssh_host_key_fingerprint,
			approved_at=EXCLUDED.approved_at,updated_at=now()`, device.ID, workerID,
		fingerprint, device.ApprovedAt)
	if err != nil {
		problem(c, http.StatusConflict, "设备与 Worker 绑定失败", err)
		return
	}
	if err := tx.Commit(); err != nil {
		problem(c, http.StatusInternalServerError, "确认设备绑定失败", err)
		return
	}
	device.Machines = []clientMachine{{WorkerID: workerID,
		SSHHostKeyFingerprint: fingerprint, ApprovedAt: device.ApprovedAt}}
	c.JSON(http.StatusOK, device)
}

func (s *Server) rejectClientDevicePairing(c *gin.Context) {
	administratorID := c.MustGet("session").(auth.Session).AdministratorID
	pairingID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	result, err := s.db.ExecContext(c.Request.Context(), `
		UPDATE client_device_pairings SET status='rejected',updated_at=now()
		WHERE id=$1 AND administrator_id=$2 AND status IN ('waiting_scan','waiting_confirmation')`,
		pairingID, administratorID)
	if err != nil {
		problem(c, http.StatusInternalServerError, "拒绝设备绑定失败", err)
		return
	}
	updated, err := result.RowsAffected()
	if err != nil {
		problem(c, http.StatusInternalServerError, "拒绝设备绑定失败", err)
		return
	}
	if updated != 1 {
		problem(c, http.StatusNotFound, "设备绑定不存在", nil)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) deleteClientDevice(c *gin.Context) {
	administratorID := c.MustGet("session").(auth.Session).AdministratorID
	deviceID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	result, err := s.db.ExecContext(c.Request.Context(),
		"DELETE FROM client_devices WHERE id=$1 AND administrator_id=$2",
		deviceID, administratorID)
	if err != nil {
		problem(c, http.StatusInternalServerError, "删除设备失败", err)
		return
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		problem(c, http.StatusInternalServerError, "删除设备失败", err)
		return
	}
	if deleted != 1 {
		problem(c, http.StatusNotFound, "设备不存在", nil)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) loadAdministratorPairing(c *gin.Context, pairingID uuid.UUID) (clientDevicePairing, error) {
	administratorID := c.MustGet("session").(auth.Session).AdministratorID
	var pairing clientDevicePairing
	err := s.db.QueryRowContext(c.Request.Context(), `
		SELECT pairing.id,pairing.status,pairing.device_id,pairing.device_name,pairing.platform,
			pairing.worker_id,worker.name,pairing.ssh_host_key_fingerprint,
			pairing.expires_at,pairing.created_at
		FROM client_device_pairings pairing JOIN workers worker ON worker.id=pairing.worker_id
		WHERE pairing.id=$1 AND pairing.administrator_id=$2`,
		pairingID, administratorID).
		Scan(&pairing.ID, &pairing.Status, &pairing.DeviceID, &pairing.DeviceName,
			&pairing.Platform, &pairing.WorkerID, &pairing.WorkerName,
			&pairing.SSHHostKeyFingerprint, &pairing.ExpiresAt, &pairing.CreatedAt)
	if err == nil && time.Now().UTC().After(pairing.ExpiresAt) &&
		pairing.Status != "approved" && pairing.Status != "rejected" {
		pairing.Status = "expired"
	}
	return pairing, err
}

func (s *Server) authenticateClientDevice(ctx context.Context, token string) (auth.Session, error) {
	deviceID, ok := parseClientDeviceToken(token)
	if !ok {
		return auth.Session{}, auth.ErrSessionInvalid
	}
	var session auth.Session
	err := s.db.QueryRowContext(ctx, `
		UPDATE client_devices d SET last_seen_at=now()
		FROM administrators a
		WHERE d.administrator_id=a.id AND d.id=$1 AND d.credential_hash=$2
		RETURNING a.id,a.username`, deviceID, security.Digest(token)).
		Scan(&session.AdministratorID, &session.Username)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.Session{}, auth.ErrSessionInvalid
	}
	if err != nil {
		return auth.Session{}, err
	}
	session.Token = token
	return session, nil
}

func parseClientDeviceToken(token string) (uuid.UUID, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != "tdv1" || len(parts[2]) < 16 {
		return uuid.Nil, false
	}
	deviceID, err := uuid.Parse(parts[1])
	if err != nil || deviceID == uuid.Nil {
		return uuid.Nil, false
	}
	return deviceID, true
}

func devicePairingURI(serverURL string, serverID, pairingID uuid.UUID, secret string,
	workerID uuid.UUID, workerName, sshHostKeyFingerprint string, expiresAt time.Time,
) string {
	query := url.Values{}
	query.Set("v", "3")
	query.Set("server", strings.TrimRight(serverURL, "/"))
	query.Set("serverId", serverID.String())
	query.Set("pairingId", pairingID.String())
	query.Set("secret", secret)
	query.Set("workerId", workerID.String())
	query.Set("workerName", workerName)
	query.Set("sshHostKeyFingerprint", sshHostKeyFingerprint)
	query.Set("expiresAt", expiresAt.UTC().Format(time.RFC3339))
	return "tyrshand://device-pair?" + query.Encode()
}

func qrDataURL(content string) (string, error) {
	code, err := qr.Encode(content, qr.M, qr.Auto)
	if err != nil {
		return "", err
	}
	scaled, err := barcode.Scale(code, 320, 320)
	if err != nil {
		return "", err
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, scaled); err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buffer.Bytes()), nil
}
