package httpapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/slovx2/tyrs-hand/internal/auth"
)

const clientMaxAttachmentBytes = 25 * 1024 * 1024

type clientAttachment struct {
	ID               uuid.UUID  `json:"id"`
	SessionID        *uuid.UUID `json:"sessionId"`
	Kind             string     `json:"kind"`
	OriginalFilename string     `json:"filename"`
	MediaType        string     `json:"mediaType"`
	SizeBytes        int64      `json:"sizeBytes"`
	SHA256           string     `json:"sha256"`
	Status           string     `json:"status"`
	CreatedAt        time.Time  `json:"createdAt"`
}

func scanClientAttachment(row rowScanner) (clientAttachment, error) {
	var item clientAttachment
	var sessionID sql.NullString
	err := row.Scan(&item.ID, &sessionID, &item.Kind, &item.OriginalFilename,
		&item.MediaType, &item.SizeBytes, &item.SHA256, &item.Status, &item.CreatedAt)
	if err == nil && sessionID.Valid {
		id, parseErr := uuid.Parse(sessionID.String)
		if parseErr != nil {
			return clientAttachment{}, parseErr
		}
		item.SessionID = &id
	}
	return item, err
}

const clientAttachmentColumns = `id,session_id::text,kind,original_filename,media_type,
	size_bytes,sha256,status,created_at`

func (s *Server) clientUpload(c *gin.Context) {
	failed := true
	defer func() {
		if failed {
			clientUploadFailures.Inc()
		}
	}()
	deviceID, ok := clientRequestDeviceID(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body,
		clientMaxAttachmentBytes+1024*1024)
	header, err := c.FormFile("file")
	if err != nil || header.Size <= 0 || header.Size > clientMaxAttachmentBytes {
		badRequest(c, errors.New("附件为空或超过 25 MiB"))
		return
	}
	localID := strings.TrimSpace(c.PostForm("localId"))
	if localID == "" || len(localID) > 200 {
		badRequest(c, errors.New("附件 localId 无效"))
		return
	}
	sourceKey := deviceID.String() + ":" + localID
	existing, scanErr := scanClientAttachment(s.db.QueryRowContext(c.Request.Context(),
		`SELECT `+clientAttachmentColumns+` FROM session_attachments
		WHERE source_type='client' AND source_key=$1`, sourceKey))
	if scanErr == nil {
		failed = false
		c.JSON(http.StatusOK, gin.H{"attachment": existing, "deduplicated": true})
		return
	}
	if !errors.Is(scanErr, sql.ErrNoRows) {
		problem(c, http.StatusInternalServerError, "检查附件幂等键失败", scanErr)
		return
	}
	source, err := header.Open()
	if err != nil {
		problem(c, http.StatusBadRequest, "读取附件失败", err)
		return
	}
	defer func() { _ = source.Close() }()
	attachmentID := uuid.New()
	storageKey := filepath.ToSlash(filepath.Join("client", attachmentID.String()[:2],
		attachmentID.String()))
	target, err := s.clientAttachmentPath(storageKey)
	if err != nil {
		problem(c, http.StatusInternalServerError, "生成附件路径失败", err)
		return
	}
	if err = os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		problem(c, http.StatusInternalServerError, "创建附件目录失败", err)
		return
	}
	destination, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		problem(c, http.StatusInternalServerError, "创建附件失败", err)
		return
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(destination, hash),
		io.LimitReader(source, clientMaxAttachmentBytes+1))
	closeErr := destination.Close()
	if copyErr != nil || closeErr != nil || written != header.Size || written > clientMaxAttachmentBytes {
		_ = os.Remove(target)
		badRequest(c, errors.New("附件上传不完整"))
		return
	}
	filename := strings.TrimSpace(filepath.Base(strings.ReplaceAll(header.Filename, "\x00", "")))
	if filename == "" || filename == "." {
		filename = "attachment"
	}
	mediaType := strings.TrimSpace(header.Header.Get("Content-Type"))
	if parsed, _, parseErr := mime.ParseMediaType(mediaType); parseErr == nil {
		mediaType = parsed
	}
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	kind := "file"
	if strings.HasPrefix(mediaType, "image/") {
		kind = "image"
	}
	created, err := scanClientAttachment(s.db.QueryRowContext(c.Request.Context(),
		`INSERT INTO session_attachments(id,uploaded_by_device_id,source_type,source_key,
		kind,original_filename,media_type,size_bytes,sha256,storage_key)
		VALUES ($1,$2,'client',$3,$4,$5,$6,$7,$8,$9) RETURNING `+clientAttachmentColumns,
		attachmentID, deviceID, sourceKey, kind, filename, mediaType, written,
		hex.EncodeToString(hash.Sum(nil)), storageKey))
	if err != nil {
		_ = os.Remove(target)
		problem(c, http.StatusInternalServerError, "保存附件失败", err)
		return
	}
	failed = false
	c.JSON(http.StatusCreated, gin.H{"attachment": created, "deduplicated": false})
}

func (s *Server) clientDownloadAttachment(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	var storageKey, filename, mediaType, digest string
	var size int64
	administrator := c.MustGet("session").(auth.Session)
	err = s.db.QueryRowContext(c.Request.Context(), `SELECT attachment.storage_key,
		attachment.original_filename,attachment.media_type,attachment.size_bytes,attachment.sha256
		FROM session_attachments attachment
		LEFT JOIN development_sessions session ON session.id=attachment.session_id
		LEFT JOIN client_devices device ON device.id=attachment.uploaded_by_device_id
		WHERE attachment.id=$1 AND attachment.status<>'deleted'
		AND (session.created_by_administrator_id=$2 OR device.administrator_id=$2)`,
		id, administrator.AdministratorID).
		Scan(&storageKey, &filename, &mediaType, &size, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusNotFound, "附件不存在", err)
		return
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取附件失败", err)
		return
	}
	target, err := s.clientAttachmentPath(storageKey)
	if err != nil {
		problem(c, http.StatusInternalServerError, "附件路径无效", err)
		return
	}
	file, err := os.Open(target)
	if err != nil {
		problem(c, http.StatusNotFound, "附件文件不存在", err)
		return
	}
	defer func() { _ = file.Close() }()
	c.Header("Cache-Control", "private, no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("X-Attachment-SHA256", digest)
	c.Header("Content-Disposition", mime.FormatMediaType("attachment",
		map[string]string{"filename": filename}))
	c.DataFromReader(http.StatusOK, size, mediaType, file, nil)
}

func (s *Server) clientAttachmentPath(storageKey string) (string, error) {
	root, err := filepath.Abs(s.cfg.AttachmentRoot)
	if err != nil {
		return "", err
	}
	target := filepath.Join(root, filepath.FromSlash(storageKey))
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("附件存储路径越界")
	}
	return target, nil
}

func linkClientAttachmentsTx(ctx context.Context, tx *sql.Tx, sessionID, messageID, deviceID uuid.UUID,
	attachmentIDs []uuid.UUID,
) error {
	if len(attachmentIDs) == 0 {
		return nil
	}
	ids := make([]string, 0, len(attachmentIDs))
	for _, id := range attachmentIDs {
		ids = append(ids, id.String())
	}
	var count int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM session_attachments WHERE id=ANY($1::uuid[])
		AND uploaded_by_device_id=$2 AND status='uploaded'`, pq.Array(ids), deviceID).Scan(&count)
	if err != nil {
		return err
	}
	if count != len(ids) {
		return errors.New("附件不存在、已使用或不属于当前设备")
	}
	for index, id := range attachmentIDs {
		if _, err = tx.ExecContext(ctx, `INSERT INTO session_message_attachments(message_id,attachment_id,ordinal)
			VALUES ($1,$2,$3)`, messageID, id, index); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE session_attachments SET session_id=$2,status='attached',
		attached_at=now() WHERE id=ANY($1::uuid[])`, pq.Array(ids), sessionID)
	return err
}
