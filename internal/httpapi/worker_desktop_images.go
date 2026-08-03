package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/slovx2/tyrs-hand/internal/workerregistry"
)

const desktopImageUploadOverhead = 1 << 20

type desktopImageRecord struct {
	id               uuid.UUID
	ordinal          int
	originalFilename string
	discordFilename  string
	mediaType        string
	size             int64
	sha256           string
	status           string
	error            string
	attachmentID     string
}

type desktopImageTarget struct {
	channelID     string
	messageID     string
	projectionKey string
	card          discordintegration.ComponentCardPayload
}

type desktopImageDiscord interface {
	UploadDesktopImage(context.Context, string, string,
		discordintegration.ComponentCardPayload, string, string, io.Reader) (string, error)
	UpdateDesktopCard(context.Context, string, string,
		discordintegration.ComponentCardPayload) error
	Close(context.Context)
}

func prepareDesktopImages(images []workerprotocol.DesktopImage,
	notice string,
) ([]desktopImageRecord, []string) {
	result := make([]desktopImageRecord, 0, min(len(images), workerprotocol.DesktopImageCountLimit))
	failures := make([]string, 0)
	total := int64(0)
	for index, metadata := range images {
		if index >= workerprotocol.DesktopImageCountLimit {
			break
		}
		filename := safeDesktopImageFilename(metadata.Filename, index)
		item := desktopImageRecord{id: uuid.New(), ordinal: index,
			originalFilename: filename, mediaType: metadata.MediaType,
			size: metadata.Size, sha256: strings.ToLower(metadata.SHA256), status: "pending"}
		failure := strings.TrimSpace(metadata.Error)
		if failure == "" && !validDesktopImageMetadata(metadata, filename) {
			failure = "图片元数据无效"
		}
		if failure == "" && total+metadata.Size > workerprotocol.DesktopImageTotalLimit {
			failure = "图片合计大小超过限制"
		}
		if failure == "" {
			total += metadata.Size
			item.discordFilename = safeDiscordImageFilename(index, item.sha256, filename)
		} else {
			item.status, item.error = "failed", failure
			failures = append(failures, filename+"（"+failure+"）")
		}
		result = append(result, item)
	}
	if len(images) > workerprotocol.DesktopImageCountLimit {
		failures = append(failures, fmt.Sprintf("另有 %d 张图片超过数量限制",
			len(images)-workerprotocol.DesktopImageCountLimit))
	}
	if strings.TrimSpace(notice) != "" {
		failures = append(failures, strings.TrimSpace(notice))
	}
	return result, failures
}

func safeDiscordImageFilename(index int, digest, filename string) string {
	extension := strings.ToLower(filepath.Ext(filename))
	base := strings.TrimSuffix(filename, filepath.Ext(filename))
	var cleaned strings.Builder
	for _, character := range base {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '-' || character == '_' {
			cleaned.WriteRune(character)
		} else if cleaned.Len() == 0 || !strings.HasSuffix(cleaned.String(), "-") {
			cleaned.WriteByte('-')
		}
		if cleaned.Len() >= 60 {
			break
		}
	}
	safeBase := strings.Trim(cleaned.String(), "-")
	if safeBase == "" {
		safeBase = "image"
	}
	return fmt.Sprintf("%02d-%s-%s%s", index+1, digest[:12], safeBase, extension)
}

func insertDesktopImagesTx(ctx context.Context, tx *sql.Tx, intentID uuid.UUID,
	images []desktopImageRecord,
) error {
	for _, image := range images {
		_, err := tx.ExecContext(ctx, `INSERT INTO desktop_turn_images
			(id,intent_id,ordinal,original_filename,discord_filename,media_type,
			 size_bytes,sha256,status,error)
			VALUES ($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''),NULLIF($7,0),
			 NULLIF($8,''),$9,NULLIF($10,''))`, image.id, intentID, image.ordinal,
			image.originalFilename, image.discordFilename, image.mediaType,
			image.size, image.sha256, image.status, image.error)
		if err != nil {
			return err
		}
	}
	return nil
}

func safeDesktopImageFilename(value string, index int) string {
	value = filepath.Base(strings.TrimSpace(value))
	if value == "" || value == "." || value == ".." || len(value) > 180 ||
		strings.ContainsAny(value, "/\\") {
		return fmt.Sprintf("image-%d.invalid", index+1)
	}
	return value
}

func validDesktopImageMetadata(image workerprotocol.DesktopImage, filename string) bool {
	if image.Error != "" || image.Size <= 0 || image.Size > workerprotocol.DesktopImageFileLimit ||
		len(image.SHA256) != sha256.Size*2 {
		return false
	}
	if _, err := hex.DecodeString(image.SHA256); err != nil {
		return false
	}
	return desktopImageTypeMatches(filename, image.MediaType)
}

func desktopImageTypeMatches(filename, mediaType string) bool {
	extension := strings.ToLower(filepath.Ext(filename))
	switch mediaType {
	case "image/png":
		return extension == ".png"
	case "image/jpeg":
		return extension == ".jpg" || extension == ".jpeg"
	case "image/gif":
		return extension == ".gif"
	case "image/webp":
		return extension == ".webp"
	default:
		return false
	}
}

func (s *Server) workerDesktopImageTarget(c *gin.Context) {
	intentID, ok := desktopImageIntentID(c)
	if !ok {
		return
	}
	status, err := s.desktopImageTargetStatus(c.Request.Context(), currentWorker(c), intentID)
	if errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusNotFound, "Desktop 图片不存在", err)
		return
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Desktop 图片目标失败", err)
		return
	}
	c.JSON(http.StatusOK, workerprotocol.DesktopImageTarget{Status: status})
}

func (s *Server) desktopImageTargetStatus(ctx context.Context, worker workerregistry.Worker,
	intentID uuid.UUID,
) (string, error) {
	var pending int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE image.status='pending')
		FROM codex_turn_intents intent
		JOIN codex_turn_runs run ON run.primary_intent_id=intent.id
		LEFT JOIN desktop_turn_images image ON image.intent_id=intent.id
		WHERE intent.id=$1 AND run.worker_id=$2`, intentID, worker.ID).Scan(&pending); err != nil {
		return "", err
	}
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM codex_turn_intents intent
		JOIN codex_turn_runs run ON run.primary_intent_id=intent.id
		WHERE intent.id=$1 AND run.worker_id=$2)`, intentID, worker.ID).Scan(&exists); err != nil {
		return "", err
	}
	if !exists {
		return "", sql.ErrNoRows
	}
	if pending == 0 {
		return "complete", nil
	}
	_, err := s.resolveDesktopImageTarget(ctx, s.db, intentID)
	if errors.Is(err, sql.ErrNoRows) {
		return "waiting", nil
	}
	if err != nil {
		return "", err
	}
	return "ready", nil
}

func desktopImageIntentID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, errors.New("desktop intent ID 无效"))
		return uuid.Nil, false
	}
	return id, true
}

func desktopImageOrdinal(c *gin.Context) (int, bool) {
	var ordinal int
	if _, err := fmt.Sscanf(c.Param("ordinal"), "%d", &ordinal); err != nil ||
		ordinal < 0 || ordinal >= workerprotocol.DesktopImageCountLimit {
		badRequest(c, errors.New("desktop 图片顺序无效"))
		return 0, false
	}
	return ordinal, true
}

type validatedDesktopImageReader struct {
	source    io.Reader
	size      int64
	remaining int64
	digest    hash.Hash
	expected  string
	checked   bool
}

func (r *validatedDesktopImageReader) Size() int64 { return r.size }

func (r *validatedDesktopImageReader) Finalize() error {
	if r.remaining != 0 {
		return errors.New("图片大小与元数据不一致")
	}
	if !r.checked {
		r.checked = true
	}
	if hex.EncodeToString(r.digest.Sum(nil)) != r.expected {
		return errors.New("图片 SHA-256 与元数据不一致")
	}
	return nil
}

func (r *validatedDesktopImageReader) Read(buffer []byte) (int, error) {
	if r.remaining > 0 {
		if int64(len(buffer)) > r.remaining {
			buffer = buffer[:r.remaining]
		}
		n, err := r.source.Read(buffer)
		if n > 0 {
			r.remaining -= int64(n)
			_, _ = r.digest.Write(buffer[:n])
		}
		if err == io.EOF && r.remaining > 0 {
			err = io.ErrUnexpectedEOF
		}
		return n, err
	}
	if r.checked {
		return 0, io.EOF
	}
	r.checked = true
	var extra [1]byte
	if n, err := r.source.Read(extra[:]); n > 0 || (err != nil && err != io.EOF) {
		return 0, errors.New("图片大小与元数据不一致")
	}
	if hex.EncodeToString(r.digest.Sum(nil)) != r.expected {
		return 0, errors.New("图片 SHA-256 与元数据不一致")
	}
	return 0, io.EOF
}

func (s *Server) workerUploadDesktopImage(c *gin.Context) {
	intentID, ok := desktopImageIntentID(c)
	if !ok {
		return
	}
	ordinal, ok := desktopImageOrdinal(c)
	if !ok {
		return
	}
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "开始 Desktop 图片上传失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	image, err := lockDesktopImage(c.Request.Context(), tx, currentWorker(c), intentID, ordinal)
	if errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusNotFound, "Desktop 图片不存在", err)
		return
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Desktop 图片失败", err)
		return
	}
	if image.status == "delivered" {
		c.JSON(http.StatusOK, workerprotocol.DesktopImageUploadResult{
			Status: "delivered", AttachmentID: image.attachmentID})
		return
	}
	if image.status == "failed" {
		problem(c, http.StatusConflict, "Desktop 图片已标记失败", errors.New(image.error))
		return
	}
	target, err := s.resolveDesktopImageTarget(c.Request.Context(), tx, intentID)
	if errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusTooEarly, "Discord 消息尚未创建", err)
		return
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Discord 图片目标失败", err)
		return
	}
	metadata, stream, err := readDesktopImageUpload(c, image)
	if err != nil {
		markErr := markDesktopImageFailed(c.Request.Context(), tx, intentID, ordinal, err.Error())
		if markErr == nil {
			markErr = tx.Commit()
		}
		if markErr != nil {
			problem(c, http.StatusInternalServerError, "记录 Desktop 图片校验失败", markErr)
			return
		}
		badRequest(c, err)
		return
	}
	card, err := desktopImageCard(c.Request.Context(), tx, intentID, target.card, &image)
	if err != nil {
		problem(c, http.StatusInternalServerError, "生成 Discord 图片卡片失败", err)
		return
	}
	remote, err := s.openDesktopImageRemote(c.Request.Context())
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Discord 配置失败", err)
		return
	}
	defer remote.Close(context.Background())
	attachmentID, uploadErr := remote.UploadDesktopImage(c.Request.Context(),
		target.channelID, target.messageID, card, image.discordFilename,
		image.originalFilename, stream)
	status := "pending"
	if uploadErr == nil {
		status = "delivered"
	} else if metadata.FinalAttempt {
		status = "failed"
	}
	_, err = tx.ExecContext(c.Request.Context(), `UPDATE desktop_turn_images SET
		status=$3, attempt_count=attempt_count+1,
		discord_attachment_id=CASE WHEN $3='delivered' THEN NULLIF($4,'')
			ELSE discord_attachment_id END,
		error=CASE WHEN $3='delivered' THEN NULL ELSE NULLIF($5,'') END,
		delivered_at=CASE WHEN $3='delivered' THEN now() ELSE delivered_at END,
		updated_at=now() WHERE intent_id=$1 AND ordinal=$2`, intentID, ordinal,
		status, attachmentID, errorText(uploadErr))
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "记录 Desktop 图片上传结果失败", err)
		return
	}
	if uploadErr != nil {
		if status == "failed" {
			s.projectDesktopImageFailure(c.Request.Context(), intentID)
		}
		problem(c, http.StatusBadGateway, "上传 Desktop 图片到 Discord 失败", uploadErr)
		return
	}
	c.JSON(http.StatusOK, workerprotocol.DesktopImageUploadResult{
		Status: "delivered", AttachmentID: attachmentID})
}

func (s *Server) workerFailDesktopImage(c *gin.Context) {
	intentID, ok := desktopImageIntentID(c)
	if !ok {
		return
	}
	ordinal, ok := desktopImageOrdinal(c)
	if !ok {
		return
	}
	var request workerprotocol.DesktopImageFailureRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	message := strings.TrimSpace(request.Error)
	if message == "" {
		message = "图片同步失败"
	}
	result, err := s.db.ExecContext(c.Request.Context(), `UPDATE desktop_turn_images image SET
		status='failed', error=$4, updated_at=now()
		FROM codex_turn_intents intent
		WHERE image.intent_id=$1 AND image.ordinal=$2 AND image.status='pending'
		AND intent.id=image.intent_id AND EXISTS(SELECT 1 FROM codex_turn_runs run
			WHERE run.primary_intent_id=intent.id AND run.worker_id=$3)`,
		intentID, ordinal, currentWorker(c).ID, message)
	if err != nil {
		problem(c, http.StatusInternalServerError, "记录 Desktop 图片失败状态失败", err)
		return
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		var exists bool
		if err := s.db.QueryRowContext(c.Request.Context(), `SELECT EXISTS(SELECT 1
			FROM desktop_turn_images image WHERE image.intent_id=$1 AND image.ordinal=$2
			AND EXISTS(SELECT 1 FROM codex_turn_runs run
				WHERE run.primary_intent_id=image.intent_id AND run.worker_id=$3))`,
			intentID, ordinal, currentWorker(c).ID).
			Scan(&exists); err != nil {
			problem(c, http.StatusInternalServerError, "读取 Desktop 图片状态失败", err)
			return
		}
		if !exists {
			problem(c, http.StatusNotFound, "Desktop 图片不存在", sql.ErrNoRows)
			return
		}
	}
	s.projectDesktopImageFailure(c.Request.Context(), intentID)
	c.JSON(http.StatusOK, workerprotocol.DesktopImageUploadResult{Status: "failed"})
}

func lockDesktopImage(ctx context.Context, tx *sql.Tx, worker workerregistry.Worker,
	intentID uuid.UUID, ordinal int,
) (desktopImageRecord, error) {
	var image desktopImageRecord
	var attachmentID sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT image.id, image.ordinal,
		image.original_filename, COALESCE(image.discord_filename,''),
		COALESCE(image.media_type,''), COALESCE(image.size_bytes,0),
		COALESCE(image.sha256,''), image.status, COALESCE(image.error,''),
		image.discord_attachment_id
		FROM desktop_turn_images image WHERE image.intent_id=$1 AND image.ordinal=$2
		AND EXISTS(SELECT 1 FROM codex_turn_runs run
			WHERE run.primary_intent_id=image.intent_id AND run.worker_id=$3)
		FOR UPDATE`, intentID, ordinal, worker.ID).Scan(&image.id, &image.ordinal,
		&image.originalFilename, &image.discordFilename, &image.mediaType, &image.size,
		&image.sha256, &image.status, &image.error, &attachmentID)
	if err == nil && attachmentID.Valid {
		image.attachmentID = attachmentID.String
	}
	return image, err
}

func markDesktopImageFailed(ctx context.Context, tx *sql.Tx, intentID uuid.UUID,
	ordinal int, message string,
) error {
	_, err := tx.ExecContext(ctx, `UPDATE desktop_turn_images SET status='failed',
		error=$3, attempt_count=attempt_count+1, updated_at=now()
		WHERE intent_id=$1 AND ordinal=$2`, intentID, ordinal, message)
	return err
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func readDesktopImageUpload(c *gin.Context, image desktopImageRecord) (
	workerprotocol.DesktopImageUploadMetadata, io.Reader, error,
) {
	var metadata workerprotocol.DesktopImageUploadMetadata
	mediaType, parameters, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" || parameters["boundary"] == "" {
		return metadata, nil, errors.New("desktop 图片上传必须使用 multipart/form-data")
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body,
		workerprotocol.DesktopImageFileLimit+desktopImageUploadOverhead)
	reader := multipart.NewReader(c.Request.Body, parameters["boundary"])
	part, err := reader.NextPart()
	if err != nil || part.FormName() != "metadata" {
		return metadata, nil, errors.New("desktop 图片上传缺少 metadata")
	}
	encoded, err := io.ReadAll(io.LimitReader(part, 64<<10))
	_ = part.Close()
	if err != nil || json.Unmarshal(encoded, &metadata) != nil {
		return metadata, nil, errors.New("desktop 图片上传 metadata 无效")
	}
	part, err = reader.NextPart()
	if err != nil || part.FormName() != "file" {
		return metadata, nil, errors.New("desktop 图片上传缺少 file")
	}
	prefixSize := min(image.size, 512)
	prefix := make([]byte, prefixSize)
	if _, err := io.ReadFull(part, prefix); err != nil {
		return metadata, nil, errors.New("desktop 图片内容不足")
	}
	actualType := http.DetectContentType(prefix)
	if actualType != image.mediaType ||
		!desktopImageTypeMatches(image.originalFilename, actualType) {
		return metadata, nil, errors.New("desktop 图片 MIME 与元数据不一致")
	}
	stream := &validatedDesktopImageReader{
		source: io.MultiReader(bytes.NewReader(prefix), part), size: image.size, remaining: image.size,
		digest: sha256.New(), expected: image.sha256,
	}
	return metadata, stream, nil
}

type desktopImageQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Server) resolveDesktopImageTarget(ctx context.Context, query desktopImageQueryer,
	intentID uuid.UUID,
) (desktopImageTarget, error) {
	var target desktopImageTarget
	var desired []byte
	err := query.QueryRowContext(ctx, `SELECT projection.resource_id,
		COALESCE(projection.message_id,''), projection.projection_key,
		projection.desired_payload
		FROM codex_turn_intents intent
		JOIN codex_thread_controls control ON control.id=intent.control_id
		JOIN discord_conversations conversation
			ON conversation.id=COALESCE(intent.discord_conversation_id,
				control.discord_conversation_id)
		JOIN discord_projections projection ON projection.guild_id=conversation.guild_id
			AND projection.projection_key='desktop-input:' || conversation.id::text || ':' ||
				COALESCE(NULLIF(intent.projection_anchor,''),
					NULLIF(intent.desktop_input_projection_key,''), intent.id::text) || ':0'
		WHERE intent.id=$1 AND COALESCE(projection.message_id,'')<>''`, intentID).
		Scan(&target.channelID, &target.messageID, &target.projectionKey, &desired)
	if err != nil {
		return target, err
	}
	var payload struct {
		Card discordintegration.ComponentCardPayload `json:"card"`
	}
	if err := json.Unmarshal(desired, &payload); err != nil {
		return target, err
	}
	target.card = payload.Card
	return target, nil
}

func desktopImageCard(ctx context.Context, query desktopImageQueryer, intentID uuid.UUID,
	base discordintegration.ComponentCardPayload, current *desktopImageRecord,
) (discordintegration.ComponentCardPayload, error) {
	rows, err := query.QueryContext(ctx, `SELECT ordinal, original_filename,
		COALESCE(discord_filename,''), status, COALESCE(error,'')
		FROM desktop_turn_images WHERE intent_id=$1 ORDER BY ordinal`, intentID)
	if err != nil {
		return base, err
	}
	defer func() { _ = rows.Close() }()
	base.Media = nil
	failures := make([]string, 0)
	for rows.Next() {
		var ordinal int
		var original, discordName, status, failure string
		if err := rows.Scan(&ordinal, &original, &discordName, &status, &failure); err != nil {
			return base, err
		}
		if status == "delivered" || (current != nil && current.ordinal == ordinal) {
			base.Media = append(base.Media, discordintegration.ComponentMediaPayload{
				Filename: discordName, Description: original})
		}
		if status == "failed" {
			if failure == "" {
				failure = "图片同步失败"
			}
			failures = append(failures, original+"（"+failure+"）")
		}
	}
	if err := rows.Err(); err != nil {
		return base, err
	}
	if len(failures) > 0 {
		base.Sections = append(base.Sections, "**图片同步失败：** "+strings.Join(failures, "、"))
	}
	return base, nil
}

func (s *Server) openDesktopImageRemote(ctx context.Context) (desktopImageDiscord, error) {
	if s.desktopImageRemote != nil {
		return s.desktopImageRemote(ctx)
	}
	if s.discord == nil {
		return nil, errors.New("discord 尚未配置")
	}
	token, err := s.discord.BotToken(ctx)
	if err != nil {
		return nil, err
	}
	return discordintegration.NewDisgoRemote(token, "", nil), nil
}

func (s *Server) projectDesktopImageFailure(ctx context.Context, intentID uuid.UUID) {
	target, err := s.resolveDesktopImageTarget(ctx, s.db, intentID)
	if err != nil {
		return
	}
	card, err := desktopImageCard(ctx, s.db, intentID, target.card, nil)
	if err != nil {
		return
	}
	remote, err := s.openDesktopImageRemote(ctx)
	if err != nil {
		return
	}
	defer remote.Close(context.Background())
	if err := remote.UpdateDesktopCard(ctx, target.channelID, target.messageID, card); err != nil &&
		s.logger != nil {
		s.logger.Warn("更新 Desktop 图片失败提示失败")
	}
}
