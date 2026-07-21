package httpapi

import (
	"errors"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

func (s *Server) workerDownloadAttachment(c *gin.Context) {
	runID, ok := parseRunID(c)
	if !ok {
		return
	}
	attachmentID, err := uuid.Parse(c.Param("attachmentId"))
	if err != nil {
		badRequest(c, err)
		return
	}
	epoch, err := strconv.ParseInt(c.GetHeader("X-Run-Lease-Epoch"), 10, 64)
	if err != nil {
		badRequest(c, errors.New("附件请求缺少 Run Lease Epoch"))
		return
	}
	lease := workerprotocol.RunLeaseRequest{LeaseToken: c.GetHeader("X-Run-Lease-Token"),
		LeaseEpoch: epoch}
	if lease.LeaseToken == "" {
		badRequest(c, errors.New("附件请求缺少 Run Lease Token"))
		return
	}
	node := workerNode(c)
	claimed, err := s.claimedRemoteRun(c.Request.Context(), node.ID, runID, lease)
	if err != nil {
		remoteRunError(c, "校验附件下载权限失败", err)
		return
	}
	var storageKey, filename, mediaType, sha256 string
	var size int64
	err = s.db.QueryRowContext(c.Request.Context(), `SELECT a.storage_key, a.original_filename,
		a.media_type, a.size_bytes, a.sha256 FROM discord_attachments a
		JOIN codex_turn_intents i ON i.discord_message_id = a.message_id
		WHERE a.id = $1 AND i.control_id = $2 AND a.status = 'ready'
		AND a.storage_key IS NOT NULL`, attachmentID, claimed.ControlID).
		Scan(&storageKey, &filename, &mediaType, &size, &sha256)
	if err != nil {
		problem(c, http.StatusNotFound, "Discord 附件不存在", err)
		return
	}
	root, err := filepath.Abs(s.cfg.AttachmentRoot)
	if err != nil {
		problem(c, http.StatusInternalServerError, "解析附件目录失败", err)
		return
	}
	target := filepath.Join(root, filepath.FromSlash(storageKey))
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		problem(c, http.StatusInternalServerError, "附件存储路径无效", err)
		return
	}
	file, err := os.Open(target)
	if err != nil {
		problem(c, http.StatusNotFound, "读取 Discord 附件失败", err)
		return
	}
	defer func() { _ = file.Close() }()
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("X-Attachment-SHA256", sha256)
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{
		"filename": filename,
	}))
	c.DataFromReader(http.StatusOK, size, mediaType, file, nil)
}
