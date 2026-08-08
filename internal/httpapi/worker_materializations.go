package httpapi

import (
	"database/sql"
	"errors"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/security"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

func (s *Server) workerClaimMaterialization(c *gin.Context) {
	worker := currentWorker(c)
	leaseToken, err := security.RandomToken(32)
	if err != nil {
		problem(c, http.StatusInternalServerError, "创建 materialization lease 失败", err)
		return
	}
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "领取 materialization 失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var task workerprotocol.MaterializationClaim
	err = tx.QueryRowContext(c.Request.Context(), `SELECT id,original_filename,media_type,
		size_bytes,sha256 FROM client_materializations
		WHERE worker_id=$1 AND (status='queued' OR
			(status='materializing' AND lease_expires_at<now()))
		ORDER BY created_at,id FOR UPDATE SKIP LOCKED LIMIT 1`, worker.ID).
		Scan(&task.ID, &task.Filename, &task.MediaType, &task.SizeBytes, &task.SHA256)
	if errors.Is(err, sql.ErrNoRows) {
		if err = tx.Commit(); err != nil {
			problem(c, http.StatusInternalServerError, "提交 materialization 空领取失败", err)
			return
		}
		c.JSON(http.StatusOK, workerprotocol.MaterializationClaimResponse{})
		return
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "领取 materialization 失败", err)
		return
	}
	task.LeaseToken = leaseToken
	task.ExpiresAt = time.Now().UTC().Add(materializationLeaseDuration(s))
	_, err = tx.ExecContext(c.Request.Context(), `UPDATE client_materializations
		SET status='materializing',lease_token_hash=$2,lease_expires_at=$3,error=NULL,
			updated_at=now() WHERE id=$1`, task.ID, security.Digest(leaseToken), task.ExpiresAt)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "提交 materialization 领取失败", err)
		return
	}
	c.JSON(http.StatusOK, workerprotocol.MaterializationClaimResponse{Materialization: &task})
}

func (s *Server) workerDownloadMaterialization(c *gin.Context) {
	id, leaseToken, ok := materializationLeaseParameters(c)
	if !ok {
		return
	}
	var storageKey, digest, filename, mediaType string
	var size int64
	err := s.db.QueryRowContext(c.Request.Context(), `SELECT storage_key,sha256,
		original_filename,media_type,size_bytes FROM client_materializations
		WHERE id=$1 AND worker_id=$2 AND status='materializing'
		  AND lease_token_hash=$3 AND lease_expires_at>now()`, id, currentWorker(c).ID,
		security.Digest(leaseToken)).Scan(&storageKey, &digest, &filename, &mediaType, &size)
	if errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusConflict, "materialization lease 已失效", err)
		return
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 materialization 失败", err)
		return
	}
	target, err := s.clientAttachmentPath(storageKey)
	if err != nil {
		problem(c, http.StatusInternalServerError, "materialization 路径无效", err)
		return
	}
	file, err := os.Open(target)
	if err != nil {
		problem(c, http.StatusNotFound, "materialization 文件不存在", err)
		return
	}
	defer func() { _ = file.Close() }()
	c.Header("X-Attachment-SHA256", digest)
	c.Header("Content-Disposition", mime.FormatMediaType("attachment",
		map[string]string{"filename": filename}))
	c.DataFromReader(http.StatusOK, size, mediaType, file, nil)
}

func (s *Server) workerCompleteMaterialization(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, errors.New("materialization id 无效"))
		return
	}
	var request workerprotocol.MaterializationCompleteRequest
	if err = c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	request.RemotePath = filepath.Clean(strings.TrimSpace(request.RemotePath))
	if request.LeaseToken == "" || !filepath.IsAbs(request.RemotePath) ||
		request.RemotePath == string(filepath.Separator) || len(request.RemotePath) > 4096 {
		badRequest(c, errors.New("materialization 完成参数无效"))
		return
	}
	result, err := s.db.ExecContext(c.Request.Context(), `UPDATE client_materializations
		SET status='completed',remote_path=$4,lease_token_hash=NULL,lease_expires_at=NULL,
			updated_at=now(),completed_at=now()
		WHERE id=$1 AND worker_id=$2 AND status='materializing'
		  AND lease_token_hash=$3 AND lease_expires_at>now()`, id, currentWorker(c).ID,
		security.Digest(request.LeaseToken), request.RemotePath)
	if err != nil {
		problem(c, http.StatusInternalServerError, "完成 materialization 失败", err)
		return
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		problem(c, http.StatusConflict, "materialization lease 已失效", nil)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) workerFailMaterialization(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, errors.New("materialization id 无效"))
		return
	}
	var request workerprotocol.MaterializationFailRequest
	if err = c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	request.Error = strings.TrimSpace(request.Error)
	if request.LeaseToken == "" || request.Error == "" || len(request.Error) > 2000 {
		badRequest(c, errors.New("materialization 失败参数无效"))
		return
	}
	result, err := s.db.ExecContext(c.Request.Context(), `UPDATE client_materializations
		SET status='failed',error=$4,lease_token_hash=NULL,lease_expires_at=NULL,
			updated_at=now(),completed_at=now()
		WHERE id=$1 AND worker_id=$2 AND status='materializing'
		  AND lease_token_hash=$3`, id, currentWorker(c).ID,
		security.Digest(request.LeaseToken), request.Error)
	if err != nil {
		problem(c, http.StatusInternalServerError, "记录 materialization 失败", err)
		return
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		problem(c, http.StatusConflict, "materialization lease 已失效", nil)
		return
	}
	c.Status(http.StatusNoContent)
}

func materializationLeaseParameters(c *gin.Context) (uuid.UUID, string, bool) {
	id, err := uuid.Parse(c.Param("id"))
	leaseToken := strings.TrimSpace(c.GetHeader("X-Materialization-Lease-Token"))
	if err != nil || leaseToken == "" {
		badRequest(c, errors.New("materialization lease 参数无效"))
		return uuid.Nil, "", false
	}
	return id, leaseToken, true
}

func materializationLeaseDuration(s *Server) time.Duration {
	if s.cfg.LeaseDuration > 0 {
		return s.cfg.LeaseDuration
	}
	return 2 * time.Minute
}
