package discordintegration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/officialapp"
)

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
			card, visible := officialItemCard(turn, item, latestPlan, planActionID)
			if !visible {
				continue
			}
			key := fmt.Sprintf("official:%s:%04d:%04d:%s", conversationID,
				turnIndex, itemIndex, item.ID)
			var messageID string
			var needsDelivery bool
			err = tx.QueryRowContext(ctx, `INSERT INTO discord_projections(
				guild_id,projection_key,resource_id,desired_payload)
				VALUES($1,$2,$3,$4)
				ON CONFLICT(guild_id,projection_key) DO UPDATE SET
				resource_id=EXCLUDED.resource_id,
				desired_version=discord_projections.desired_version+
					CASE WHEN discord_projections.desired_payload IS DISTINCT FROM
						EXCLUDED.desired_payload THEN 1 ELSE 0 END,
				desired_payload=EXCLUDED.desired_payload,updated_at=now()
				RETURNING COALESCE(message_id,''),desired_version>applied_version`, guildID,
				key, discordThreadID, mustJSON(map[string]any{"card": card})).
				Scan(&messageID, &needsDelivery)
			if err != nil {
				return err
			}
			operationKey := "projection:" + key
			if needsDelivery {
				operation, nonce := "message.create", key
				payload := map[string]any{"channelId": discordThreadID, "card": card}
				if messageID != "" {
					operation, nonce = "message.update", ""
					payload["messageId"] = messageID
				}
				if err = EnqueueTxAfter(ctx, tx, operationKey, operation,
					"channels/"+discordThreadID+"/messages", payload, nonce,
					predecessor); err != nil {
					return err
				}
			}
			predecessor = operationKey
		}
	}
	return tx.Commit()
}

func officialItemCard(turn officialapp.Turn, item officialapp.Item, latestPlan *officialapp.Plan,
	planActionID uuid.UUID,
) (ComponentCardPayload, bool) {
	switch item.Type {
	case "userMessage", "hookPrompt":
		return ComponentCardPayload{}, false
	case "agentMessage":
		return ComponentCardPayload{AccentColor: cardColorGreen, Header: "Codex",
			Body: cardText(SanitizeDiscordResult(item.Text), 3800)}, true
	case "plan":
		card := ComponentCardPayload{AccentColor: cardColorBlurple, Header: "📋 Codex · 执行计划",
			Body: cardText(item.Text, 3600)}
		current := latestPlan != nil && latestPlan.TurnID == turn.ID &&
			latestPlan.ItemID == item.ID && planActionID != uuid.Nil
		card.Buttons = []ComponentButtonPayload{{Label: "执行计划", Disabled: !current,
			CustomID: func() string {
				if !current {
					return ""
				}
				return planExecuteButtonPrefix + planActionID.String()
			}(), Style: "primary"}}
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
