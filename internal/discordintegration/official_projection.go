package discordintegration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/officialapp"
)

type officialProjectionContent struct {
	Suffix string
	Card   ComponentCardPayload
	Files  []MessageFilePayload
}

// ProjectOfficialThread 只按官方 turns/items 的顺序建立 Discord 投影。
// 通知抵达顺序不会进入 projection key 或 Outbox 顺序。
func ProjectOfficialThread(ctx context.Context, db *sql.DB, workspaceID uuid.UUID,
	thread officialapp.Thread,
) error {
	encoded, err := json.Marshal(thread)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `INSERT INTO official_thread_projections(
		workspace_id,thread_id,thread) VALUES($1,$2,$3)
		ON CONFLICT(workspace_id,thread_id) DO UPDATE SET thread=EXCLUDED.thread,
		revision=official_thread_projections.revision+1,observed_at=now()`, workspaceID,
		thread.ID, encoded)
	if err != nil {
		return err
	}
	var conversationID uuid.UUID
	var guildID, discordThreadID string
	err = tx.QueryRowContext(ctx, `SELECT binding.conversation_id,
		conversation.guild_id,conversation.thread_id FROM official_thread_bindings binding
		JOIN discord_conversations conversation ON conversation.id=binding.conversation_id
		WHERE binding.workspace_id=$1 AND binding.thread_id=$2`, workspaceID, thread.ID).
		Scan(&conversationID, &guildID, &discordThreadID)
	if errors.Is(err, sql.ErrNoRows) {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	owner := "external"
	lastTurnID, lastClientID := thread.LatestClientMessage()
	if strings.HasPrefix(lastClientID, "discord:") ||
		strings.HasPrefix(lastClientID, "discord-plan:") {
		owner = "control"
	}
	_, err = tx.ExecContext(ctx, `UPDATE official_thread_bindings SET
		interactive_owner=$3,owned_turn_id=NULLIF($4,''),
		last_client_message_id=NULLIF($5,''),updated_at=now()
		WHERE workspace_id=$1 AND thread_id=$2`, workspaceID, thread.ID, owner,
		lastTurnID, lastClientID)
	if err != nil {
		return err
	}
	latestPlan := thread.LatestCompletedPlan()
	var planActionID uuid.UUID
	if latestPlan == nil {
		_, err = tx.ExecContext(ctx, `UPDATE official_plan_actions SET status='stale'
			WHERE conversation_id=$1 AND status='available'`, conversationID)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE official_plan_actions SET status='stale'
			WHERE conversation_id=$1 AND status='available'
			AND (thread_id,turn_id,item_id)<>($2,$3,$4)`, conversationID, thread.ID,
			latestPlan.TurnID, latestPlan.ItemID)
		if err == nil {
			err = tx.QueryRowContext(ctx, `INSERT INTO official_plan_actions(
				workspace_id,conversation_id,thread_id,turn_id,item_id,plan_text)
				VALUES($1,$2,$3,$4,$5,$6)
				ON CONFLICT(workspace_id,thread_id,turn_id,item_id) DO UPDATE SET
				plan_text=EXCLUDED.plan_text
				RETURNING id`, workspaceID, conversationID, thread.ID, latestPlan.TurnID,
				latestPlan.ItemID, latestPlan.Text).Scan(&planActionID)
		}
	}
	if err != nil {
		return err
	}
	predecessor := ""
	for turnIndex, turn := range thread.Turns {
		for itemIndex, item := range turn.Items {
			for _, content := range officialItemProjections(turn, item, latestPlan, planActionID) {
				key := fmt.Sprintf("official:%s:%04d:%04d:%s", conversationID,
					turnIndex, itemIndex, content.Suffix)
				if err = projectOfficialContent(ctx, tx, guildID, discordThreadID, key,
					content, predecessor); err != nil {
					return err
				}
				predecessor = "projection:" + key
			}
		}
		if turn.Status == "failed" {
			content := officialTurnErrorProjection(thread.ID, turn)
			key := fmt.Sprintf("official:%s:%04d:%04d:turn-error", conversationID,
				turnIndex, len(turn.Items))
			if err = projectOfficialContent(ctx, tx, guildID, discordThreadID, key,
				content, predecessor); err != nil {
				return err
			}
			predecessor = "projection:" + key
		}
	}
	return tx.Commit()
}

func projectOfficialContent(ctx context.Context, tx *sql.Tx, guildID, discordThreadID,
	key string, content officialProjectionContent, predecessor string,
) error {
	desired := map[string]any{"card": content.Card}
	if len(content.Files) > 0 {
		desired["files"] = content.Files
	}
	var messageID string
	var needsDelivery bool
	err := tx.QueryRowContext(ctx, `INSERT INTO discord_projections(
				guild_id,projection_key,resource_id,desired_payload)
				VALUES($1,$2,$3,$4)
				ON CONFLICT(guild_id,projection_key) DO UPDATE SET
				resource_id=EXCLUDED.resource_id,
				desired_version=discord_projections.desired_version+
					CASE WHEN discord_projections.desired_payload IS DISTINCT FROM
						EXCLUDED.desired_payload THEN 1 ELSE 0 END,
				desired_payload=EXCLUDED.desired_payload,updated_at=now()
				RETURNING COALESCE(message_id,''),desired_version>applied_version`, guildID,
		key, discordThreadID, mustJSON(desired)).
		Scan(&messageID, &needsDelivery)
	if err != nil || !needsDelivery {
		return err
	}
	operation, nonce := "message.create", key
	payload := map[string]any{"channelId": discordThreadID, "card": content.Card}
	if len(content.Files) > 0 {
		payload["files"] = content.Files
	}
	if messageID != "" {
		operation, nonce = "message.update", ""
		payload["messageId"] = messageID
	}
	return EnqueueTxAfter(ctx, tx, "projection:"+key, operation,
		"channels/"+discordThreadID+"/messages", payload, nonce, predecessor)
}

func officialItemCard(turn officialapp.Turn, item officialapp.Item, latestPlan *officialapp.Plan,
	planActionID uuid.UUID,
) (ComponentCardPayload, bool) {
	projections := officialItemProjections(turn, item, latestPlan, planActionID)
	if len(projections) == 0 {
		return ComponentCardPayload{}, false
	}
	return projections[0].Card, true
}

func officialItemProjections(turn officialapp.Turn, item officialapp.Item,
	latestPlan *officialapp.Plan, planActionID uuid.UUID,
) []officialProjectionContent {
	switch item.Type {
	case "hookPrompt":
		return nil
	case "userMessage":
		return officialUserMessageProjections(item)
	case "imageGeneration":
		return []officialProjectionContent{officialImageGenerationProjection(item)}
	default:
		card, visible := officialStandardItemCard(turn, item, latestPlan, planActionID)
		if !visible {
			return nil
		}
		return []officialProjectionContent{{Suffix: item.ID, Card: card}}
	}
}

func officialStandardItemCard(turn officialapp.Turn, item officialapp.Item,
	latestPlan *officialapp.Plan, planActionID uuid.UUID,
) (ComponentCardPayload, bool) {
	switch item.Type {
	case "agentMessage":
		return ComponentCardPayload{AccentColor: cardColorGreen, Header: "Codex",
			Body: cardText(SanitizeDiscordResult(item.Text), 3800)}, true
	case "plan":
		card := ComponentCardPayload{AccentColor: cardColorBlurple, Header: "📋 Codex · 执行计划",
			Body: cardText(item.Text, 3600)}
		current := latestPlan != nil && latestPlan.TurnID == turn.ID &&
			latestPlan.ItemID == item.ID && planActionID != uuid.Nil
		if current {
			card.Buttons = []ComponentButtonPayload{{Label: "执行计划",
				CustomID: planExecuteButtonPrefix + planActionID.String(), Style: "primary"}}
		}
		return card, true
	case "reasoning":
		return ComponentCardPayload{AccentColor: cardColorGray, Header: "Codex · 推理",
			Body: cardText(officialReasoningText(item.Raw), 1600)}, true
	default:
		return ComponentCardPayload{AccentColor: cardColorBlurple,
			Header: "Codex · " + officialItemLabel(item.Type),
			Body:   cardText(officialActivityText(item.Raw), 1800)}, true
	}
}

func officialUserMessageProjections(item officialapp.Item) []officialProjectionContent {
	if item.ClientID != nil && (strings.HasPrefix(*item.ClientID, "discord:") ||
		strings.HasPrefix(*item.ClientID, "discord-plan:")) {
		return nil
	}
	var inputs []struct {
		Type string `json:"type"`
		Text string `json:"text"`
		Path string `json:"path"`
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	parseErr := len(item.Content) > 0 && json.Unmarshal(item.Content, &inputs) != nil
	parts := make([]string, 0, len(inputs))
	for _, input := range inputs {
		switch input.Type {
		case "text":
			if strings.TrimSpace(input.Text) != "" {
				parts = append(parts, input.Text)
			}
		case "localImage":
			parts = append(parts, "🖼️ 本地图片：`"+cardText(input.Path, 900)+"`")
		case "mention":
			label := input.Name
			if label == "" {
				label = input.Path
			}
			parts = append(parts, "📎 引用：`"+cardText(label, 900)+"`")
		case "image", "audio", "localAudio", "skill":
			label := input.Name
			if label == "" {
				label = input.Path
			}
			if label == "" {
				label = input.URL
			}
			parts = append(parts, "📎 "+input.Type+"：`"+cardText(label, 900)+"`")
		}
	}
	text := strings.TrimSpace(strings.Join(parts, "\n\n"))
	if text == "" {
		text = strings.TrimSpace(item.Text)
	}
	if parseErr {
		text = "（官方用户输入无法解析）"
	} else if text == "" {
		text = "（无文本输入）"
	}
	text = discordSecretPattern.ReplaceAllString(text, "[已隐藏凭据]")
	pages := splitCardText(text, desktopInputPageRunes)
	result := make([]officialProjectionContent, 0, len(pages))
	for index, page := range pages {
		header := "👤 外部客户端输入"
		suffix := item.ID
		if len(pages) > 1 {
			header += fmt.Sprintf(" · %d/%d", index+1, len(pages))
			suffix += fmt.Sprintf(":page-%04d", index)
		}
		result = append(result, officialProjectionContent{Suffix: suffix,
			Card: ComponentCardPayload{AccentColor: cardColorBlurple, Header: header, Body: page}})
	}
	return result
}

func officialImageGenerationProjection(item officialapp.Item) officialProjectionContent {
	var value struct {
		Status        string  `json:"status"`
		RevisedPrompt *string `json:"revisedPrompt"`
		Result        string  `json:"result"`
	}
	if json.Unmarshal(item.Raw, &value) != nil {
		return generatedImageError(item.ID, "官方 imageGeneration Item 无法解析")
	}
	body := "状态：`" + cardText(value.Status, 100) + "`"
	if value.RevisedPrompt != nil && strings.TrimSpace(*value.RevisedPrompt) != "" {
		body += "\n\n" + cardText(*value.RevisedPrompt, 1400)
	}
	card := ComponentCardPayload{AccentColor: cardColorBlurple,
		Header: "🖼️ Codex · 生成图片", Body: body}
	if strings.TrimSpace(value.Result) == "" {
		return officialProjectionContent{Suffix: item.ID, Card: card}
	}
	file, err := generatedImageFile(value.Result)
	if err != nil {
		return generatedImageError(item.ID, err.Error())
	}
	card.AccentColor = cardColorGreen
	card.Media = []ComponentMediaPayload{{Filename: file.Filename,
		Description: "Codex 生成图片"}}
	return officialProjectionContent{Suffix: item.ID, Card: card,
		Files: []MessageFilePayload{file}}
}

func generatedImageFile(encoded string) (MessageFilePayload, error) {
	encoded = strings.TrimSpace(encoded)
	if int64(base64.StdEncoding.DecodedLen(len(encoded))) > DefaultMaxFileBytes {
		return MessageFilePayload{}, errors.New("生成图片超过 Discord 25 MiB 限制")
	}
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return MessageFilePayload{}, errors.New("生成图片不是合法 Base64")
	}
	if int64(len(data)) > DefaultMaxFileBytes {
		return MessageFilePayload{}, errors.New("生成图片超过 Discord 25 MiB 限制")
	}
	mediaType, extension := generatedImageMediaType(data)
	if mediaType == "" {
		return MessageFilePayload{}, errors.New("生成图片不是受支持的 PNG/JPEG/GIF/WebP")
	}
	digest := sha256.Sum256(data)
	return MessageFilePayload{Filename: "codex-generated-" +
		hex.EncodeToString(digest[:]) + extension, MediaType: mediaType, Base64: encoded}, nil
}

func generatedImageMediaType(data []byte) (string, string) {
	switch {
	case bytes.HasPrefix(data, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}):
		return "image/png", ".png"
	case len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff:
		return "image/jpeg", ".jpg"
	case bytes.HasPrefix(data, []byte("GIF87a")) || bytes.HasPrefix(data, []byte("GIF89a")):
		return "image/gif", ".gif"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp", ".webp"
	default:
		return "", ""
	}
}

func generatedImageError(itemID, message string) officialProjectionContent {
	return officialProjectionContent{Suffix: itemID, Card: ComponentCardPayload{
		AccentColor: cardColorRed, Header: "❌ Codex · 生成图片投影失败",
		Body: cardText(message, 1800),
	}}
}

func officialTurnErrorProjection(threadID string, turn officialapp.Turn) officialProjectionContent {
	details := ComponentErrorPayload{Message: "官方 Turn 执行失败",
		ThreadID: threadID, TurnID: turn.ID}
	if turn.Error != nil {
		details.Message = discordSecretPattern.ReplaceAllString(
			strings.TrimSpace(turn.Error.Message), "[已隐藏凭据]")
		details.CodexErrorInfo = turn.Error.CodexErrorInfo
		if turn.Error.AdditionalDetails != nil {
			details.AdditionalDetails = discordSecretPattern.ReplaceAllString(
				strings.TrimSpace(*turn.Error.AdditionalDetails), "[已隐藏凭据]")
		}
	}
	return officialProjectionContent{Suffix: "turn-error", Card: ComponentCardPayload{
		AccentColor: cardColorRed, Header: "❌ Codex · 执行失败", Error: &details,
	}}
}

func officialReasoningText(raw json.RawMessage) string {
	var value struct {
		Summary []string `json:"summary"`
		Content []string `json:"content"`
	}
	_ = json.Unmarshal(raw, &value)
	text := strings.Join(value.Summary, "\n")
	if text == "" {
		text = strings.Join(value.Content, "\n")
	}
	return text
}

func officialActivityText(raw json.RawMessage) string {
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		return "状态已更新"
	}
	parts := make([]string, 0, 4)
	for _, key := range []string{"command", "tool", "status", "cwd"} {
		if text, ok := value[key].(string); ok && strings.TrimSpace(text) != "" {
			parts = append(parts, "**"+key+"**  `"+cardText(text, 700)+"`")
		}
	}
	if len(parts) == 0 {
		return "状态已更新"
	}
	return strings.Join(parts, "\n")
}

func officialItemLabel(kind string) string {
	return strings.ReplaceAll(kind, "_", " ")
}
