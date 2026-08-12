package discordintegration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

const conversationImageLimit = 10

type conversationImagePayload struct {
	AttachmentID string `json:"attachmentId"`
	Filename     string `json:"filename"`
	Description  string `json:"description"`
	MediaType    string `json:"mediaType"`
	SizeBytes    int64  `json:"sizeBytes"`
	SHA256       string `json:"sha256"`
	StorageKey   string `json:"storageKey"`
}

func ProjectConversationImages(ctx context.Context, db *sql.DB, threadID string,
	conversationID uuid.UUID, inputMessageID string, runID uuid.UUID,
	attachmentIDs []uuid.UUID,
) error {
	if len(attachmentIDs) == 0 {
		return nil
	}
	if len(attachmentIDs) > conversationImageLimit {
		return errors.New("Discord 回复图片不能超过 10 张")
	}
	var guildID string
	var sessionID uuid.UUID
	if err := db.QueryRowContext(ctx, `SELECT guild_id,session_id FROM discord_conversations
		WHERE id=$1 AND thread_id=$2 AND session_id IS NOT NULL`, conversationID, threadID).
		Scan(&guildID, &sessionID); err != nil {
		return err
	}
	images := make([]conversationImagePayload, 0, len(attachmentIDs))
	media := make([]ComponentMediaPayload, 0, len(attachmentIDs))
	seen := make(map[uuid.UUID]bool, len(attachmentIDs))
	for ordinal, attachmentID := range attachmentIDs {
		if attachmentID == uuid.Nil || seen[attachmentID] {
			return errors.New("Discord 回复图片引用无效或重复")
		}
		seen[attachmentID] = true
		var image conversationImagePayload
		var attachmentSessionID uuid.UUID
		var sourceKey string
		if err := db.QueryRowContext(ctx, `SELECT session_id,source_key,original_filename,
			media_type,size_bytes,sha256,storage_key FROM session_attachments
			WHERE id=$1 AND source_type='agent' AND kind='image'
				AND status IN ('uploaded','attached')`, attachmentID).
			Scan(&attachmentSessionID, &sourceKey, &image.Description, &image.MediaType,
				&image.SizeBytes, &image.SHA256, &image.StorageKey); err != nil {
			return fmt.Errorf("读取 Discord 回复图片 %s: %w", attachmentID, err)
		}
		if attachmentSessionID != sessionID ||
			!strings.HasPrefix(sourceKey, runID.String()+":") {
			return errors.New("Discord 回复图片不属于当前 Run")
		}
		if !validConversationImage(image) {
			return errors.New("Discord 回复图片元数据无效")
		}
		image.AttachmentID = attachmentID.String()
		image.Filename = conversationImageFilename(ordinal, attachmentID, image.SHA256,
			image.MediaType)
		images = append(images, image)
		media = append(media, ComponentMediaPayload{Filename: image.Filename,
			Description: image.Description})
	}
	key := "conversation-reply:" + conversationID.String() + ":message:" +
		inputMessageID + ":images"
	payload := map[string]any{"channelId": threadID, "images": images,
		"card": ComponentCardPayload{AccentColor: cardColorBlurple,
			Header: "Codex · 图片", Media: media}}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var messageID string
	if err := tx.QueryRowContext(ctx, `INSERT INTO discord_projections
		(guild_id,projection_key,resource_id,desired_payload) VALUES ($1,$2,$3,$4)
		ON CONFLICT(guild_id,projection_key) DO UPDATE SET
		resource_id=EXCLUDED.resource_id,desired_payload=EXCLUDED.desired_payload,
		desired_version=discord_projections.desired_version+1,updated_at=now()
		RETURNING COALESCE(message_id,'')`, guildID, key, threadID,
		mustJSON(payload)).Scan(&messageID); err != nil {
		return err
	}
	if messageID != "" {
		payload["messageId"] = messageID
	}
	if err := enqueueDiscordOutbox(ctx, tx, "projection:"+key, "message.images",
		"channels/"+threadID+"/messages", payload, key); err != nil {
		return err
	}
	return tx.Commit()
}

func validConversationImage(image conversationImagePayload) bool {
	if image.SizeBytes <= 0 || image.SizeBytes > 25<<20 || len(image.SHA256) != 64 ||
		strings.TrimSpace(image.StorageKey) == "" || filepath.IsAbs(image.StorageKey) {
		return false
	}
	for _, character := range image.SHA256 {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	switch image.MediaType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

func conversationImageFilename(ordinal int, id uuid.UUID, digest, mediaType string) string {
	extension := map[string]string{"image/png": ".png", "image/jpeg": ".jpg",
		"image/gif": ".gif", "image/webp": ".webp"}[mediaType]
	return fmt.Sprintf("%02d-%s-%s%s", ordinal+1, id.String()[:8], digest[:12], extension)
}
