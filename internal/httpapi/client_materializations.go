package httpapi

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type clientMaterialization struct {
	ID         uuid.UUID
	Workspace  uuid.UUID
	Filename   string
	MediaType  string
	SizeBytes  int64
	SHA256     string
	StorageKey string
	Status     string
	RemotePath sql.NullString
	Failure    sql.NullString
}

const clientMaxAttachmentBytes = 25 * 1024 * 1024

type rowScanner interface {
	Scan(dest ...any) error
}

func (s *Server) clientAttachmentPath(storageKey string) (string, error) {
	root, err := filepath.Abs(s.cfg.AttachmentRoot)
	if err != nil {
		return "", err
	}
	target := filepath.Join(root, filepath.FromSlash(storageKey))
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("附件存储路径越界")
	}
	return target, nil
}

func (s *Server) clientCreateMaterialization(c *gin.Context) {
	deviceID, ok := clientRequestDeviceID(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body,
		clientMaxAttachmentBytes+1024*1024)
	workspaceID, err := uuid.Parse(strings.TrimSpace(c.PostForm("workspaceId")))
	if err != nil {
		badRequest(c, errors.New("materialization workspaceId 无效"))
		return
	}
	clientID := strings.TrimSpace(c.PostForm("clientId"))
	if clientID == "" || len(clientID) > 200 {
		badRequest(c, errors.New("materialization clientId 无效"))
		return
	}
	sourceKey := deviceID.String() + ":" + clientID
	var workerID uuid.UUID
	err = s.db.QueryRowContext(c.Request.Context(), `SELECT workspace.worker_id
		FROM worker_workspaces workspace JOIN workers worker ON worker.id=workspace.worker_id
		WHERE workspace.id=$1 AND worker.enabled=true`, workspaceID).Scan(&workerID)
	if err != nil {
		problem(c, http.StatusConflict, "Workspace 没有可用 Worker", err)
		return
	}
	header, err := c.FormFile("file")
	if err != nil || header.Size <= 0 || header.Size > clientMaxAttachmentBytes {
		badRequest(c, errors.New("附件为空或超过 25 MiB"))
		return
	}
	filename := materializationFilename(header.Filename)
	mediaType := materializationMediaType(header.Header.Get("Content-Type"))
	id := uuid.New()
	storageKey := filepath.ToSlash(filepath.Join("materializations", id.String()[:2], id.String()))
	target, err := s.clientAttachmentPath(storageKey)
	if err != nil {
		problem(c, http.StatusInternalServerError, "生成 materialization 路径失败", err)
		return
	}
	if err = os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		problem(c, http.StatusInternalServerError, "创建 materialization 目录失败", err)
		return
	}
	digest, size, err := writeClientMaterialization(header, target)
	if err != nil {
		badRequest(c, err)
		return
	}
	result, err := s.db.ExecContext(c.Request.Context(), `INSERT INTO client_materializations(
		id,device_id,workspace_id,worker_id,source_type,source_key,client_id,
		original_filename,media_type,size_bytes,sha256,storage_key)
		VALUES ($1,$2,$3,$4,'mobile',$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT(source_type,source_key) DO NOTHING`, id, deviceID, workspaceID, workerID,
		sourceKey, clientID, filename, mediaType, size, digest, storageKey)
	if err != nil {
		_ = os.Remove(target)
		problem(c, http.StatusInternalServerError, "保存 materialization 失败", err)
		return
	}
	inserted, _ := result.RowsAffected()
	if inserted == 0 {
		_ = os.Remove(target)
	}
	item, err := s.waitClientMaterialization(c, sourceKey)
	if err != nil {
		return
	}
	if item.Workspace != workspaceID || item.SHA256 != digest || item.SizeBytes != size {
		problem(c, http.StatusConflict, "materialization 幂等键对应不同附件", nil)
		return
	}
	inputType := "mention"
	if strings.HasPrefix(item.MediaType, "image/") {
		inputType = "localImage"
	}
	c.JSON(http.StatusCreated, gin.H{"attachment": gin.H{
		"id": item.ID, "sha256": item.SHA256, "filename": item.Filename,
		"mediaType": item.MediaType, "sizeBytes": item.SizeBytes,
		"remotePath": item.RemotePath.String, "inputType": inputType,
	}, "deduplicated": inserted == 0})
}

func writeClientMaterialization(header *multipart.FileHeader, target string) (string, int64, error) {
	source, err := header.Open()
	if err != nil {
		return "", 0, errors.New("读取附件失败")
	}
	defer func() { _ = source.Close() }()
	destination, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(destination, hash),
		io.LimitReader(source, clientMaxAttachmentBytes+1))
	closeErr := destination.Close()
	if copyErr != nil || closeErr != nil || written != header.Size || written <= 0 ||
		written > clientMaxAttachmentBytes {
		_ = os.Remove(target)
		return "", 0, errors.New("附件上传不完整")
	}
	return hex.EncodeToString(hash.Sum(nil)), written, nil
}

func materializationFilename(value string) string {
	name := strings.TrimSpace(filepath.Base(strings.ReplaceAll(value, "\x00", "")))
	if name == "" || name == "." {
		return "attachment"
	}
	return name
}

func materializationMediaType(value string) string {
	if parsed, _, err := mime.ParseMediaType(strings.TrimSpace(value)); err == nil && parsed != "" {
		return parsed
	}
	return "application/octet-stream"
}

func (s *Server) waitClientMaterialization(c *gin.Context, sourceKey string,
) (clientMaterialization, error) {
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(75 * time.Second)
	defer deadline.Stop()
	for {
		item, err := scanClientMaterialization(s.db.QueryRowContext(c.Request.Context(),
			`SELECT id,workspace_id,original_filename,media_type,size_bytes,sha256,
			storage_key,status,remote_path,error FROM client_materializations
			WHERE source_type='mobile' AND source_key=$1`, sourceKey))
		if err != nil {
			problem(c, http.StatusInternalServerError, "读取 materialization 状态失败", err)
			return item, err
		}
		switch item.Status {
		case "completed":
			return item, nil
		case "failed":
			err = errors.New(item.Failure.String)
			problem(c, http.StatusBadGateway, "Worker materialization 失败", err)
			return item, err
		}
		select {
		case <-c.Request.Context().Done():
			return item, c.Request.Context().Err()
		case <-deadline.C:
			err = errors.New("等待 Worker materialization 超时")
			problem(c, http.StatusGatewayTimeout, "等待 Worker materialization 超时", err)
			return item, err
		case <-ticker.C:
		}
	}
}

func scanClientMaterialization(row rowScanner) (clientMaterialization, error) {
	var item clientMaterialization
	err := row.Scan(&item.ID, &item.Workspace, &item.Filename, &item.MediaType,
		&item.SizeBytes, &item.SHA256, &item.StorageKey, &item.Status,
		&item.RemotePath, &item.Failure)
	return item, err
}
