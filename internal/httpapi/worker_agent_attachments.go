package httpapi

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

const agentAttachmentCountLimit = 10

func (s *Server) workerUploadAgentAttachment(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body,
		clientMaxAttachmentBytes+1024*1024)
	runID, ok := parseRunID(c)
	if !ok {
		return
	}
	worker := currentWorker(c)
	claimed, err := s.claimedRemoteRun(c.Request.Context(), worker.ID, runID)
	if err != nil {
		remoteRunError(c, "校验 agent 附件 Run 失败", err)
		return
	}
	if claimed.SourceType != codexcontrol.SourceWorkspace || claimed.SessionID == uuid.Nil {
		badRequest(c, errors.New("只有 Workspace Run 可以保存 agent 附件"))
		return
	}
	itemID := strings.TrimSpace(c.PostForm("itemId"))
	ordinal, ordinalErr := strconv.Atoi(strings.TrimSpace(c.PostForm("ordinal")))
	if itemID == "" || len(itemID) > 200 || ordinalErr != nil || ordinal < 0 ||
		ordinal >= agentAttachmentCountLimit {
		badRequest(c, errors.New("agent 附件 itemId 或 ordinal 无效"))
		return
	}
	sourceKey := runID.String() + ":" + itemID + ":" + strconv.Itoa(ordinal)
	var existingID uuid.UUID
	scanErr := s.db.QueryRowContext(c.Request.Context(), `SELECT id FROM session_attachments
		WHERE source_type='agent' AND source_key=$1`, sourceKey).Scan(&existingID)
	if scanErr == nil {
		c.JSON(http.StatusOK, workerprotocol.AgentAttachmentUploadResult{
			AttachmentID: existingID, Deduplicated: true})
		return
	}
	if !errors.Is(scanErr, sql.ErrNoRows) {
		problem(c, http.StatusInternalServerError, "查询 agent 附件失败", scanErr)
		return
	}
	header, err := c.FormFile("file")
	if err != nil || header.Size <= 0 || header.Size > clientMaxAttachmentBytes {
		badRequest(c, errors.New("agent 图片为空或超过 25 MiB"))
		return
	}
	source, err := header.Open()
	if err != nil {
		badRequest(c, errors.New("读取 agent 图片失败"))
		return
	}
	defer func() { _ = source.Close() }()
	probe := make([]byte, 512)
	read, readErr := io.ReadFull(source, probe)
	if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		badRequest(c, errors.New("读取 agent 图片头失败"))
		return
	}
	probe = probe[:read]
	mediaType := http.DetectContentType(probe)
	if mediaType != "image/png" && mediaType != "image/jpeg" &&
		mediaType != "image/gif" && mediaType != "image/webp" {
		badRequest(c, errors.New("agent 附件不是受支持的图片"))
		return
	}
	attachmentID := uuid.New()
	storageKey := filepath.ToSlash(filepath.Join("agent", attachmentID.String()[:2],
		attachmentID.String()))
	target, err := s.clientAttachmentPath(storageKey)
	if err != nil {
		problem(c, http.StatusInternalServerError, "生成 agent 附件路径失败", err)
		return
	}
	if err = os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		problem(c, http.StatusInternalServerError, "创建 agent 附件目录失败", err)
		return
	}
	destination, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		problem(c, http.StatusInternalServerError, "创建 agent 附件失败", err)
		return
	}
	digest := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(destination, digest),
		io.LimitReader(io.MultiReader(bytes.NewReader(probe), source),
			clientMaxAttachmentBytes+1))
	closeErr := destination.Close()
	if copyErr != nil || closeErr != nil || written != header.Size || written > clientMaxAttachmentBytes {
		_ = os.Remove(target)
		badRequest(c, errors.New("agent 附件上传不完整"))
		return
	}
	filename := strings.TrimSpace(filepath.Base(strings.ReplaceAll(header.Filename, "\x00", "")))
	if filename == "" || filename == "." {
		filename = "agent-image"
	}
	_, err = s.db.ExecContext(c.Request.Context(), `INSERT INTO session_attachments(
		id,session_id,source_type,source_key,kind,original_filename,media_type,
		size_bytes,sha256,storage_key,status) VALUES ($1,$2,'agent',$3,'image',$4,$5,$6,$7,$8,'uploaded')`,
		attachmentID, claimed.SessionID, sourceKey, filename, mediaType, written,
		hex.EncodeToString(digest.Sum(nil)), storageKey)
	if err != nil {
		_ = os.Remove(target)
		if lookupErr := s.db.QueryRowContext(c.Request.Context(), `SELECT id FROM session_attachments
			WHERE source_type='agent' AND source_key=$1`, sourceKey).Scan(&existingID); lookupErr == nil {
			c.JSON(http.StatusOK, workerprotocol.AgentAttachmentUploadResult{
				AttachmentID: existingID, Deduplicated: true})
			return
		}
		problem(c, http.StatusInternalServerError, "保存 agent 附件失败", err)
		return
	}
	c.JSON(http.StatusCreated, workerprotocol.AgentAttachmentUploadResult{
		AttachmentID: attachmentID, Deduplicated: false})
}
