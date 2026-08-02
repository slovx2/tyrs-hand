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
	ID         uuid.UUID  `json:"id"`
	Name       string     `json:"name"`
	Platform   string     `json:"platform"`
	CreatedAt  time.Time  `json:"createdAt"`
	ApprovedAt time.Time  `json:"approvedAt"`
	LastSeenAt *time.Time `json:"lastSeenAt,omitempty"`
}

type clientDevicePairing struct {
	ID         uuid.UUID  `json:"id"`
	Status     string     `json:"status"`
	DeviceID   *uuid.UUID `json:"deviceId,omitempty"`
	DeviceName *string    `json:"deviceName,omitempty"`
	Platform   *string    `json:"platform,omitempty"`
	ExpiresAt  time.Time  `json:"expiresAt"`
	CreatedAt  time.Time  `json:"createdAt"`
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
	for rows.Next() {
		var item clientDevice
		if err := rows.Scan(&item.ID, &item.Name, &item.Platform, &item.CreatedAt,
			&item.ApprovedAt, &item.LastSeenAt); err != nil {
			problem(c, http.StatusInternalServerError, "读取设备列表失败", err)
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		problem(c, http.StatusInternalServerError, "读取设备列表失败", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (s *Server) createClientDevicePairing(c *gin.Context) {
	administratorID := c.MustGet("session").(auth.Session).AdministratorID
	secret, err := security.RandomToken(32)
	if err != nil {
		problem(c, http.StatusInternalServerError, "创建设备绑定失败", err)
		return
	}
	pairing := clientDevicePairing{ID: uuid.New(), Status: "waiting_scan",
		ExpiresAt: time.Now().UTC().Add(clientDevicePairingLifetime), CreatedAt: time.Now().UTC()}
	_, err = s.db.ExecContext(c.Request.Context(), `
		INSERT INTO client_device_pairings(id,administrator_id,pairing_secret_hash,expires_at,created_at)
		VALUES ($1,$2,$3,$4,$5)`, pairing.ID, administratorID, security.Digest(secret),
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
	pairingURI := devicePairingURI(s.cfg.PublicURL, serverID, pairing.ID, secret, pairing.ExpiresAt)
	qrDataURL, err := qrDataURL(pairingURI)
	if err != nil {
		problem(c, http.StatusInternalServerError, "生成设备二维码失败", err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"id": pairing.ID, "status": pairing.Status, "expiresAt": pairing.ExpiresAt,
		"createdAt": pairing.CreatedAt, "serverId": serverID,
		"pairingUri": pairingURI, "qrDataUrl": qrDataURL,
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
			AND NOT EXISTS (SELECT 1 FROM client_devices WHERE id=$2 OR credential_hash=$5)`,
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
	err = tx.QueryRowContext(c.Request.Context(), `
		UPDATE client_device_pairings SET status='approved',confirmed_at=now(),updated_at=now()
		WHERE id=$1 AND administrator_id=$2 AND status='waiting_confirmation' AND expires_at>now()
		RETURNING device_id,device_name,platform,now(),now()`, pairingID, administratorID).
		Scan(&device.ID, &device.Name, &device.Platform, &device.CreatedAt, &device.ApprovedAt)
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
		FROM client_device_pairings WHERE id=$1 AND administrator_id=$2`,
		pairingID, administratorID, device.ApprovedAt)
	if err != nil {
		problem(c, http.StatusConflict, "设备已存在，无法重复确认", err)
		return
	}
	if err := tx.Commit(); err != nil {
		problem(c, http.StatusInternalServerError, "确认设备绑定失败", err)
		return
	}
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
		SELECT id,status,device_id,device_name,platform,expires_at,created_at
		FROM client_device_pairings WHERE id=$1 AND administrator_id=$2`,
		pairingID, administratorID).
		Scan(&pairing.ID, &pairing.Status, &pairing.DeviceID, &pairing.DeviceName,
			&pairing.Platform, &pairing.ExpiresAt, &pairing.CreatedAt)
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
	expiresAt time.Time,
) string {
	query := url.Values{}
	query.Set("v", "2")
	query.Set("server", strings.TrimRight(serverURL, "/"))
	query.Set("serverId", serverID.String())
	query.Set("pairingId", pairingID.String())
	query.Set("secret", secret)
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
