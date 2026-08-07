package discordintegration

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

const desktopInputPageRunes = 3500

func FormatDesktopProjectionInput(input string, params json.RawMessage, failures []string) string {
	var value struct {
		Input []struct {
			Type string `json:"type"`
			Path string `json:"path"`
		} `json:"input"`
	}
	input = strings.TrimSpace(input)
	if json.Unmarshal(params, &value) == nil {
		for _, item := range value.Input {
			if item.Type == "localImage" && strings.TrimSpace(item.Path) != "" {
				input = strings.ReplaceAll(input, item.Path, filepath.Base(item.Path))
			}
		}
	}
	if len(failures) > 0 {
		input += "\n\n**图片同步失败：** " + strings.Join(failures, "、")
	}
	return strings.TrimSpace(input)
}

// DesktopInputCards 把 Desktop 用户输入转换为稳定分页的 Discord 身份卡片。
func DesktopInputCards(displayName, input string) []ComponentCardPayload {
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		displayName = "Desktop"
	}
	input = strings.TrimSpace(input)
	if input == "" {
		input = "（无文本输入）"
	}
	runes := []rune(input)
	pageCount := (len(runes) + desktopInputPageRunes - 1) / desktopInputPageRunes
	cards := make([]ComponentCardPayload, 0, pageCount)
	for page := 0; page < pageCount; page++ {
		start := page * desktopInputPageRunes
		end := min(start+desktopInputPageRunes, len(runes))
		card := ComponentCardPayload{
			AccentColor: 0x5865F2,
			Header:      "🖥️ " + displayName + " · Desktop",
			Body:        string(runes[start:end]),
		}
		if pageCount > 1 {
			card.Header += fmt.Sprintf(" · %d/%d", page+1, pageCount)
		}
		cards = append(cards, card)
	}
	return cards
}

type desktopInputExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// EnqueueDesktopInputPages 用确定性 key 投影 Desktop 输入；startPage 用于跳过 Forum Starter。
func EnqueueDesktopInputPages(ctx context.Context, execer desktopInputExecer, threadID string,
	conversationID uuid.UUID, projectionKey, displayName, input string, startPage int,
) error {
	cards := DesktopInputCards(displayName, input)
	if startPage < 0 {
		startPage = 0
	}
	var guildID, starterMessageID string
	if err := execer.QueryRowContext(ctx, `SELECT guild_id, COALESCE(starter_message_id,'')
		FROM discord_conversations WHERE id = $1`, conversationID).Scan(&guildID, &starterMessageID); err != nil {
		return err
	}
	base := fmt.Sprintf("desktop-input:%s:%s:", conversationID, projectionKey)
	for page := 0; page < len(cards); page++ {
		key := base + strconv.Itoa(page)
		var messageID string
		err := execer.QueryRowContext(ctx, `INSERT INTO discord_projections
			(guild_id, projection_key, resource_id, message_id, desired_payload)
			VALUES ($1,$2,$3,NULLIF($4,''),$5)
			ON CONFLICT(guild_id, projection_key) DO UPDATE SET
			resource_id=EXCLUDED.resource_id, desired_payload=EXCLUDED.desired_payload,
			desired_version=discord_projections.desired_version+1, updated_at=now()
			RETURNING COALESCE(message_id,'')`, guildID, key, threadID,
			func() string {
				if page == 0 && startPage > 0 {
					return starterMessageID
				}
				return ""
			}(), mustJSON(map[string]any{"card": cards[page]})).Scan(&messageID)
		if err != nil {
			return err
		}
		if page < startPage {
			if _, err := execer.ExecContext(ctx, `UPDATE discord_projections SET
				applied_version=desired_version, applied_at=COALESCE(applied_at,now()),
				last_error=NULL, updated_at=now()
				WHERE guild_id=$1 AND projection_key=$2`, guildID, key); err != nil {
				return err
			}
			continue
		}
		operation, nonce := "message.create", key
		payload := map[string]any{"channelId": threadID, "card": cards[page]}
		if messageID != "" {
			operation, nonce = "message.update", ""
			payload["messageId"] = messageID
		}
		if err := enqueueDiscordOutbox(ctx, execer, "projection:"+key, operation,
			"channels/"+threadID+"/messages", payload, nonce); err != nil {
			return err
		}
	}
	rows, err := execer.QueryContext(ctx, `SELECT projection_key, COALESCE(message_id,'')
		FROM discord_projections WHERE guild_id=$1 AND projection_key LIKE $2`, guildID, base+"%")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var key, messageID string
		if err := rows.Scan(&key, &messageID); err != nil {
			return err
		}
		page, parseErr := strconv.Atoi(strings.TrimPrefix(key, base))
		if parseErr != nil || page < len(cards) {
			continue
		}
		if messageID != "" {
			if err := enqueueDiscordOutbox(ctx, execer, "projection-delete:"+key,
				"message.delete", "channels/"+threadID+"/messages/"+messageID,
				map[string]any{"channelId": threadID, "messageId": messageID}, ""); err != nil {
				return err
			}
		}
		if _, err := execer.ExecContext(ctx, `DELETE FROM discord_projections
			WHERE guild_id=$1 AND projection_key=$2`, guildID, key); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return nil
}
